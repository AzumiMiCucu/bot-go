package src

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// =================================================================
// CAPTURE STORY/STATUS SALURAN
//
// Tujuan: menangkap STRUKTUR ASLI sebuah story saluran WA supaya bisa
// direplikasi oleh command `upswc`. Story saluran bisa tiba lewat dua jalur:
//   1. events.NewsletterLiveUpdate  → update/timeline saluran yang diikuti.
//   2. events.Message @ status@broadcast → jika story disiarkan sbg status
//      yang diatribusikan ke saluran.
// Keduanya di-dump ke tmp/capture_channel_status/*.json.
//
// Tiap file berisi:
//   • metadata (JID, tipe, id, timestamp)
//   • messageJSON     → protojson (mudah dibaca)
//   • messageProtoB64 → base64(proto.Marshal) untuk fidelitas byte-akurat
// =================================================================

const captureDir = "tmp/capture_channel_status"

// perShapeCap: berapa banyak file disimpan per "bentuk" struktur pesan yang sama.
// Bentuk BARU (mis. wrapper story yang belum pernah terlihat) selalu tertangkap,
// tapi post biasa yang berulang tidak membanjiri folder.
const perShapeCap = 4

var (
	captureMu     sync.Mutex
	captureSeq    int
	captureShapes = map[string]int{}
	captureNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
)

type channelStatusCapture struct {
	CapturedAt      string          `json:"capturedAt"`
	Source          string          `json:"source"`
	NewsletterJID   string          `json:"newsletterJID,omitempty"`
	Chat            string          `json:"chat,omitempty"`
	Sender          string          `json:"sender,omitempty"`
	MessageID       string          `json:"messageID,omitempty"`
	MessageServerID int             `json:"messageServerID,omitempty"`
	Type            string          `json:"type,omitempty"`
	Timestamp       string          `json:"timestamp,omitempty"`
	TopLevelFields  []string        `json:"topLevelFields,omitempty"`
	MessageJSON     json.RawMessage `json:"messageJSON,omitempty"`
	MessageProtoB64 string          `json:"messageProtoB64,omitempty"`
}

// dumpCapture menulis satu kejadian ke file JSON.
func dumpCapture(c channelStatusCapture, msg *waE2E.Message) {
	if msg != nil {
		if raw, err := proto.Marshal(msg); err == nil {
			c.MessageProtoB64 = base64.StdEncoding.EncodeToString(raw)
		}
		mo := protojson.MarshalOptions{Indent: "  ", EmitUnpopulated: false}
		if js, err := mo.Marshal(msg); err == nil {
			c.MessageJSON = js
		}
		c.TopLevelFields = topLevelSetFields(msg)
	}

	captureMu.Lock()
	defer captureMu.Unlock()

	// Cap per-bentuk: signature = source + daftar field top-level.
	shape := c.Source + "|" + strings.Join(c.TopLevelFields, ",")
	if captureShapes[shape] >= perShapeCap {
		return // bentuk ini sudah cukup banyak sampelnya — lewati.
	}
	captureShapes[shape]++
	captureSeq++

	if err := os.MkdirAll(captureDir, 0o755); err != nil {
		fmt.Println("[CAPTURE] ⚠️ gagal buat folder:", err)
		return
	}

	idPart := captureNameRe.ReplaceAllString(c.MessageID, "")
	if idPart == "" {
		idPart = "noid"
	}
	fname := fmt.Sprintf("%s_%03d_%s_%s.json",
		time.Now().Format("20060102-150405"), captureSeq, c.Source, idPart)
	path := filepath.Join(captureDir, fname)

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		fmt.Println("[CAPTURE] ⚠️ gagal marshal JSON:", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Println("[CAPTURE] ⚠️ gagal tulis file:", err)
		return
	}
	fmt.Printf("[CAPTURE] 📥 %s tersimpan → %s (fields: %s)\n",
		c.Source, path, strings.Join(c.TopLevelFields, ","))
}

// topLevelSetFields mengembalikan nama field proto yang TERISI di top-level
// Message — inilah petunjuk utama "wrapper" apa yang dipakai story saluran.
func topLevelSetFields(msg *waE2E.Message) []string {
	var out []string
	msg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		out = append(out, string(fd.Name()))
		return true
	})
	return out
}

// CaptureNewsletterLiveUpdate menangkap semua update saluran yang masuk.
func CaptureNewsletterLiveUpdate(evt *events.NewsletterLiveUpdate) {
	if evt == nil {
		return
	}
	for _, m := range evt.Messages {
		if m == nil {
			continue
		}
		dumpCapture(channelStatusCapture{
			CapturedAt:      time.Now().Format(time.RFC3339),
			Source:          "newsletter_live_update",
			NewsletterJID:   evt.JID.String(),
			MessageID:       string(m.MessageID),
			MessageServerID: int(m.MessageServerID),
			Type:            m.Type,
			Timestamp:       m.Timestamp.Format(time.RFC3339),
		}, m.Message)
	}
}

// CaptureStatusMessage menangkap pesan yang relevan dengan story/status saluran:
//   • dari status@broadcast (story yang disiarkan sbg status), ATAU
//   • dari server newsletter (post/story saluran dikirim sbg events.Message).
// Dipanggil dari eventHandler untuk tiap events.Message; pesan lain diabaikan.
func CaptureStatusMessage(evt *events.Message) {
	if evt == nil {
		return
	}
	isStatus := evt.Info.Chat == types.StatusBroadcastJID
	isNewsletter := evt.Info.Chat.Server == types.NewsletterServer ||
		evt.Info.Sender.Server == types.NewsletterServer
	if !isStatus && !isNewsletter {
		return
	}
	source := "newsletter_message"
	if isStatus {
		source = "status_broadcast"
	}
	dumpCapture(channelStatusCapture{
		CapturedAt:    time.Now().Format(time.RFC3339),
		Source:        source,
		NewsletterJID: evt.Info.Chat.String(),
		Chat:          evt.Info.Chat.String(),
		Sender:        evt.Info.Sender.String(),
		MessageID:     string(evt.Info.ID),
		Type:          evt.Info.Type,
		Timestamp:     evt.Info.Timestamp.Format(time.RFC3339),
	}, evt.Message)
}
