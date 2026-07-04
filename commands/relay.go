package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// RELAY TOOLKIT (owner) — 3 command dalam SATU file (crm / run / resend).
//
//   crm  [--snip]  (reply pesan) → SERIALIZE pesan yang di-reply ke JSON
//        protojson. Tanpa --snip: dikirim sebagai file <tipe>.json (untuk
//        disimpan / dipindah). Dengan --snip: JSON dibalas langsung sebagai
//        blok kode. Ini pengganti native-Go dari serialize `relayMessage(...)`.
//
//   run  (reply hasil crm: teks JSON ATAU file .json) → PARSE JSON itu lalu
//        RELAY (kirim) sebagai pesan bot ke chat ini. Pasangan dari crm.
//
//   resend / rs  (reply pesan) → gabungan crm+run seketika: langsung kirim
//        ulang isi pesan yang di-reply sebagai pesan baru bot.
//
// Semua owner-only (sejalan crm/run versi Baileys).
// =================================================================

// field konten yang diabaikan saat menentukan "tipe" pesan (bukan konten inti).
var crmIgnoredFields = map[string]bool{
	"messageContextInfo":           true,
	"senderKeyDistributionMessage": true,
	"deviceSentMessage":            true,
}

// crmSnipMaxBytes: batas ukuran JSON yang boleh dirender sebagai blok kode AIRich.
// Di atas ini payload rich jadi besar (token di-embed) → berat & bikin lag; maka
// kita fallback kirim sebagai file .json (lossless) alih-alih blok kode.
const crmSnipMaxBytes = 8000

func init() {
	RegisterCommand(Command{
		Name:        "CRM Serialize",
		Category:    "Owner",
		Aliases:     []string{"crm"},
		Pattern:     regexp.MustCompile(`(?i)^\s*crm(?:\s+(--snip|snip))?\s*$`),
		Description: "[Owner][Reply] Serialize pesan → JSON (file, atau blok kode dgn --snip)",
		Execute:     ExecuteCRM,
	})

	RegisterCommand(Command{
		Name:        "Run Relay",
		Category:    "Owner",
		Aliases:     []string{"run"},
		Pattern:     regexp.MustCompile(`(?i)^\s*run\s*$`),
		Description: "[Owner][Reply] Jalankan hasil crm (JSON/file) → relay sebagai pesan bot",
		Execute:     ExecuteRun,
	})

	RegisterCommand(Command{
		Name:        "Resend",
		Category:    "Owner",
		Aliases:     []string{"resend", "rs"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:resend|rs)\s*$`),
		Description: "[Owner][Reply] Kirim ulang pesan yang di-reply sebagai pesan baru bot",
		Execute:     ExecuteResend,
	})
}

// ----------------------------------------------------------------- crm

func ExecuteCRM(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa memakai crm.")
	}
	ci := extractContextInfo(ctx.Msg.Message)
	quoted, ok := resolveQuotedFull(ci)
	if !ok {
		return ctx.Reply("❌ Reply pesan yang ingin di-crm!")
	}

	// Serialize RAW & LENGKAP: seluruh field pesan (protojson, EmitUnpopulated agar
	// field default pun ikut terlihat) → bisa menyalin pesan PERSIS apa pun tipenya.
	jsonBytes, err := protojson.MarshalOptions{Indent: "  ", EmitUnpopulated: false}.Marshal(quoted)
	if err != nil {
		return ctx.Reply("❌ Gagal serialize pesan: " + err.Error())
	}
	innerType, label := crmContentLabel(quoted)
	snip := strings.TrimSpace(ctx.Args) != ""

	if snip {
		// --snip: tampilkan JSON sebagai blok kode ber-syntax-highlight via AIRich.
		// PENGAMAN: bila JSON terlalu besar, jangan render blok kode (berat/lag) —
		// fallback ke file .json supaya tetap utuh & ringan.
		if len(jsonBytes) > crmSnipMaxBytes {
			note := fmt.Sprintf("Relay message generator\nType : %s\n⚠️ %d byte — terlalu besar untuk blok kode, dikirim sebagai file.",
				label, len(jsonBytes))
			if err := sendJSONDocument(ctx, jsonBytes, innerType+".json", note); err != nil {
				return ctx.Reply(err.Error())
			}
			return nil
		}
		return ctx.AIRich().
			SetTitle("🧬 Relay message generator").
			AddText(fmt.Sprintf("*Type:* %s\n*Size:* %d byte", label, len(jsonBytes))).
			AddCode("json", string(jsonBytes)).
			SetFooter("crm --snip • balas dgn `run` untuk relay").
			SendToChat(ctx)
	}

	// Kirim sebagai file <tipe>.json agar bisa disimpan lalu di-`run` kapan saja.
	fileName := innerType + ".json"
	if err := sendJSONDocument(ctx, jsonBytes, fileName,
		fmt.Sprintf("Relay message generator\nType : %s", label)); err != nil {
		return ctx.Reply(err.Error())
	}
	return nil
}

// ----------------------------------------------------------------- run

func ExecuteRun(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa memakai run.")
	}
	ci := extractContextInfo(ctx.Msg.Message)
	quoted, ok := resolveQuotedFull(ci)
	if !ok {
		return ctx.Reply("❌ Reply hasil crm (teks JSON atau file .json) dulu!")
	}

	// Sumber JSON: dari file dokumen yang di-reply, atau dari teks.
	var raw []byte
	if doc := quoted.GetDocumentMessage(); doc != nil {
		mime := doc.GetMimetype()
		name := strings.ToLower(doc.GetFileName())
		if !strings.Contains(mime, "json") && !strings.HasSuffix(name, ".json") {
			return ctx.Reply("❌ File yang di-reply bukan file JSON!")
		}
		data, err := ctx.Client.Download(context.Background(), doc)
		if err != nil {
			return ctx.Reply("❌ Gagal mengunduh dokumen: " + err.Error())
		}
		raw = data
	} else {
		text := quoted.GetConversation()
		if text == "" {
			text = quoted.GetExtendedTextMessage().GetText()
		}
		if strings.TrimSpace(text) == "" {
			return ctx.Reply("❌ Pesan tidak berisi teks JSON atau file JSON!")
		}
		raw = []byte(text)
	}

	jsonStr := extractJSONObject(string(raw))
	if jsonStr == "" {
		return ctx.Reply("❌ Tidak menemukan objek JSON yang valid pada pesan yang di-reply!")
	}

	relayMsg := &waProto.Message{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(jsonStr), relayMsg); err != nil {
		return ctx.Reply("❌ JSON tidak valid / bukan hasil crm: " + err.Error())
	}

	_ = ctx.React("⏳")
	if _, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, relayMsg, AndroidExtra()); err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal relay: " + err.Error())
	}
	// Sukses → cukup react, JANGAN kirim teks (biar tak mengotori chat).
	return ctx.React("✅")
}

// ----------------------------------------------------------------- resend

func ExecuteResend(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa memakai resend.")
	}
	ci := extractContextInfo(ctx.Msg.Message)
	quoted, ok := resolveQuotedFull(ci)
	if !ok {
		return ctx.Reply("❌ Reply pesan yang ingin dikirim ulang, lalu ketik `resend`.")
	}

	_ = ctx.React("⏳")
	if _, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, quoted, AndroidExtra()); err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengirim ulang: " + err.Error())
	}
	_ = ctx.React("✅")
	return nil
}

// ----------------------------------------------------------------- helpers

// crmUnwrap membuka bungkus container berlapis (deviceSent / ephemeral / viewOnce /
// documentWithCaption / edited) untuk menemukan pesan KONTEN inti, sekaligus
// mengembalikan rantai nama bungkus yang dilewati (untuk pelabelan tipe).
// Batasi iterasi agar aman dari bungkus yang (secara teoritis) melingkar.
func crmUnwrap(m *waProto.Message) (inner *waProto.Message, chain []string) {
	for i := 0; i < 8 && m != nil; i++ {
		switch {
		case m.GetDeviceSentMessage().GetMessage() != nil:
			chain = append(chain, "deviceSentMessage")
			m = m.GetDeviceSentMessage().GetMessage()
		case m.GetEphemeralMessage().GetMessage() != nil:
			chain = append(chain, "ephemeralMessage")
			m = m.GetEphemeralMessage().GetMessage()
		case m.GetViewOnceMessage().GetMessage() != nil:
			chain = append(chain, "viewOnceMessage")
			m = m.GetViewOnceMessage().GetMessage()
		case m.GetViewOnceMessageV2().GetMessage() != nil:
			chain = append(chain, "viewOnceMessageV2")
			m = m.GetViewOnceMessageV2().GetMessage()
		case m.GetViewOnceMessageV2Extension().GetMessage() != nil:
			chain = append(chain, "viewOnceMessageV2Extension")
			m = m.GetViewOnceMessageV2Extension().GetMessage()
		case m.GetDocumentWithCaptionMessage().GetMessage() != nil:
			chain = append(chain, "documentWithCaptionMessage")
			m = m.GetDocumentWithCaptionMessage().GetMessage()
		case m.GetEditedMessage().GetMessage() != nil:
			chain = append(chain, "editedMessage")
			m = m.GetEditedMessage().GetMessage()
		default:
			return m, chain
		}
	}
	return m, chain
}

// crmContentLabel mengembalikan (innerType, label) sebuah pesan:
//   - innerType: nama field konten TERDALAM (mis. "imageMessage") → dipakai nama file.
//   - label    : rantai bungkus + inner (mis. "viewOnceMessageV2→imageMessage") → tampilan.
func crmContentLabel(m *waProto.Message) (innerType, label string) {
	inner, chain := crmUnwrap(m)
	innerType = messageContentType(inner)
	if len(chain) == 0 {
		return innerType, innerType
	}
	return innerType, strings.Join(chain, "→") + "→" + innerType
}

// messageContentType mengembalikan nama field konten pertama (mis. "imageMessage")
// dari sebuah Message (via protojson) — mengabaikan field non-konten.
func messageContentType(m *waProto.Message) string {
	if m == nil {
		return "message"
	}
	jsonBytes, err := protojson.Marshal(m)
	if err != nil {
		return "message"
	}
	var mp map[string]json.RawMessage
	if err := json.Unmarshal(jsonBytes, &mp); err != nil {
		return "message"
	}
	for k := range mp {
		if !crmIgnoredFields[k] {
			return k
		}
	}
	// hanya berisi field yang diabaikan → ambil apa pun yang ada
	for k := range mp {
		return k
	}
	return "message"
}

// extractJSONObject mengambil substring dari '{' pertama sampai '}' terakhir.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// sendJSONDocument mengunggah bytes JSON & mengirimnya sebagai dokumen (quote asal).
func sendJSONDocument(ctx *ContextBot, data []byte, fileName, caption string) error {
	upCtx, cancel := context.WithTimeout(ctx.Ctx, 60*time.Second)
	defer cancel()

	up, err := ctx.Client.Upload(upCtx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("❌ Gagal mengunggah file JSON.")
	}

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}

	msg := &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		Mimetype:      proto.String("application/json"),
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		FileName:      proto.String(fileName),
		Title:         proto.String(fileName),
		Caption:       proto.String(caption),
		ContextInfo: &waProto.ContextInfo{
			StanzaID:      proto.String(ctx.Msg.Info.ID),
			Participant:   proto.String(senderStr),
			QuotedMessage: ctx.Msg.Message,
		},
	}}
	if _, err := ctx.Client.SendMessage(ctx.Ctx, ctx.ChatJID.ToNonAD(), msg, src.AndroidExtra()); err != nil {
		return fmt.Errorf("❌ Gagal mengirim dokumen JSON.")
	}
	return nil
}
