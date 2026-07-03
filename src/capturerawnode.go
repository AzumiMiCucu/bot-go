package src

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// DIAGNOSTIK (NONAKTIF): dulu menaikkan ambang render biner XMLString agar
// plaintext node <status> dicetak HEX PENUH untuk decode protobuf story asli.
// Ternyata membebani receive-loop (tiap node masuk di-hex-encode penuh tiap
// frame), bikin bot lag/hang. Sudah tidak diperlukan — blueprint story sudah
// didapat (statusAttributions=NEWSLETTER_STATUS + messageSecret + URL).
// Aktifkan HANYA saat perlu capture ulang, dan matikan lagi setelahnya.
//
// func init() {
// 	waBinary.MaxBytesToPrintAsHex = 1 << 20
// }

// rePlaintextHex menangkap isi hex dari <plaintext ...>HEX</plaintext>.
var rePlaintextHex = regexp.MustCompile(`(?s)<plaintext[^>]*>([0-9a-fA-F]+)</plaintext>`)

// =================================================================
// CAPTURE RAW NODE (newsletter/status) — jaring level TERENDAH.
//
// client.go:830 → cli.recvLog.Debugf("%s", node.XMLString()) melog SETIAP node
// masuk (message, notification, ib, receipt, iq, ...). captureLogger mencegat
// semuanya. CaptureRawNode lama hanya memproses <message>; fungsi ini menangkap
// SEMUA node yang menyebut `newsletter` atau `status@broadcast` — termasuk node
// non-<message> yang kemungkinan membawa STORY saluran (yang tak muncul sbg
// events.Message). Disimpan mentah (XML) supaya struktur aslinya utuh.
// =================================================================

const rawNodeDumpDir = "tmp/capture_channel_status"
const rawNodePerShapeCap = 5

var (
	rawNodeDumpShapes = map[string]int{}
	rawNodeDumpMu     sync.Mutex
)

// CaptureRawNewsletterNode dipanggil untuk tiap XMLString node masuk.
func CaptureRawNewsletterNode(xml string) {
	// Hanya node yang relevan dengan saluran/status.
	low := xml
	if !strings.Contains(low, "@newsletter") &&
		!strings.Contains(low, "status@broadcast") &&
		!strings.Contains(low, `newsletter`) {
		return
	}

	// Tag root (setelah '<').
	tag := "?"
	if i := strings.IndexByte(xml, '<'); i >= 0 {
		rest := xml[i+1:]
		j := strings.IndexAny(rest, " \t\r\n>/")
		if j > 0 {
			tag = rest[:j]
		}
	}

	// Atribut root (sampai '>' pertama).
	head := xml
	if e := strings.IndexByte(xml, '>'); e >= 0 {
		head = xml[:e]
	}
	attrs := map[string]string{}
	for _, m := range reNodeAttr.FindAllStringSubmatch(head, -1) {
		attrs[m[1]] = m[2]
	}

	// Child-tag langsung (untuk signature bentuk).
	childSeen := map[string]bool{}
	var childTags []string
	for _, m := range reChildTag.FindAllStringSubmatch(xml, -1) {
		t := m[1]
		if t == tag || childSeen[t] {
			continue
		}
		childSeen[t] = true
		childTags = append(childTags, t)
	}

	// Signature bentuk = tag + type-attr + daftar child-tag. Node baru (story)
	// pasti punya signature baru → lolos cap; node berulang dibatasi.
	sortedChildren := append([]string(nil), childTags...)
	sort.Strings(sortedChildren)
	shape := tag + "|type=" + attrs["type"] + "|" + strings.Join(sortedChildren, ",")

	rawNodeDumpMu.Lock()
	if rawNodeDumpShapes[shape] >= rawNodePerShapeCap {
		rawNodeDumpMu.Unlock()
		return
	}
	rawNodeDumpShapes[shape]++
	seq := len(rawNodeDumpShapes)*1000 + rawNodeDumpShapes[shape]
	rawNodeDumpMu.Unlock()

	if err := os.MkdirAll(rawNodeDumpDir, 0o755); err != nil {
		return
	}

	rec := map[string]interface{}{
		"capturedAt": time.Now().Format(time.RFC3339),
		"kind":       "raw_node",
		"tag":        tag,
		"attrs":      attrs,
		"childTags":  childTags,
		"rawXML":     xml,
	}

	// Decode plaintext protobuf (INI kuncinya untuk story <status>): ubah hex →
	// bytes → waE2E.Message → protojson + daftar field top-level.
	if m := rePlaintextHex.FindStringSubmatch(xml); m != nil {
		if raw, err := hex.DecodeString(m[1]); err == nil {
			msg := &waE2E.Message{}
			if proto.Unmarshal(raw, msg) == nil {
				rec["decodedProtoB64"] = base64.StdEncoding.EncodeToString(raw)
				var fields []string
				msg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
					fields = append(fields, string(fd.Name()))
					return true
				})
				rec["decodedTopLevelFields"] = fields
				if js, e := (protojson.MarshalOptions{Indent: "  "}).Marshal(msg); e == nil {
					rec["decodedMessageJSON"] = json.RawMessage(js)
				}
			}
		}
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}

	id := captureNameRe.ReplaceAllString(attrs["id"], "")
	if id == "" {
		id = "noid"
	}
	fname := fmt.Sprintf("rawnode_%s_%04d_%s_%s.json",
		time.Now().Format("20060102-150405"), seq, tag, id)
	_ = os.WriteFile(filepath.Join(rawNodeDumpDir, fname), data, 0o644)
	fmt.Printf("[CAPTURE-RAW] 🧩 node <%s type=%q> child=%v → %s\n",
		tag, attrs["type"], childTags, fname)
}
