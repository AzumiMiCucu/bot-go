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
	if ci == nil || ci.GetQuotedMessage() == nil {
		return ctx.Reply("❌ Reply pesan yang ingin di-crm!")
	}
	quoted := ci.GetQuotedMessage()

	jsonBytes, err := protojson.MarshalOptions{Indent: "  "}.Marshal(quoted)
	if err != nil {
		return ctx.Reply("❌ Gagal serialize pesan: " + err.Error())
	}
	msgType := messageContentType(jsonBytes)
	snip := strings.TrimSpace(ctx.Args) != ""

	if snip {
		return ctx.Reply(fmt.Sprintf(
			"🧬 *Relay message generator*\nType : %s\n\n```json\n%s\n```",
			msgType, string(jsonBytes),
		))
	}

	// Kirim sebagai file <tipe>.json agar bisa disimpan lalu di-`run` kapan saja.
	fileName := msgType + ".json"
	if err := sendJSONDocument(ctx, jsonBytes, fileName,
		fmt.Sprintf("Relay message generator\nType : %s", msgType)); err != nil {
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
	if ci == nil || ci.GetQuotedMessage() == nil {
		return ctx.Reply("❌ Reply hasil crm (teks JSON atau file .json) dulu!")
	}
	quoted := ci.GetQuotedMessage()

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
	_ = ctx.React("✅")
	return ctx.Reply("✅ Relay berhasil dikirim!")
}

// ----------------------------------------------------------------- resend

func ExecuteResend(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa memakai resend.")
	}
	ci := extractContextInfo(ctx.Msg.Message)
	if ci == nil || ci.GetQuotedMessage() == nil {
		return ctx.Reply("❌ Reply pesan yang ingin dikirim ulang, lalu ketik `resend`.")
	}

	_ = ctx.React("⏳")
	if _, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, ci.GetQuotedMessage(), AndroidExtra()); err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengirim ulang: " + err.Error())
	}
	_ = ctx.React("✅")
	return nil
}

// ----------------------------------------------------------------- helpers

// messageContentType mengembalikan nama field konten pertama (mis. "imageMessage")
// dari JSON protojson sebuah Message — untuk label & nama file.
func messageContentType(jsonBytes []byte) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return "message"
	}
	for k := range m {
		if !crmIgnoredFields[k] {
			return k
		}
	}
	// hanya berisi field yang diabaikan → ambil apa pun yang ada
	for k := range m {
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
