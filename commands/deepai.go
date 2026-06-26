package commands

import (
	"context"
	"regexp"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// DEEPAI — edit GAMBAR / STIKER berbasis AI lewat /d/tools/deepai.
// Reply gambar/stiker + prompt → hasil edit dikirim balik. Tambahkan flag
// `--sticker` (atau `-s`) untuk mengirim hasil sebagai STIKER.
//
//   deepai <prompt>            (reply gambar/stiker)
//   deepai --sticker <prompt>  (hasil jadi stiker)
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "DeepAI Edit",
		Category:    "Tools",
		Aliases:     []string{"deepai", "editimg", "aiedit"},
		Pattern:     regexp.MustCompile(`(?i)^(?:deepai|editimg|aiedit)(?:\s+([\s\S]+))?$`),
		Description: "Edit gambar/stiker via AI (DeepAI). Reply gambar + prompt",
		Price:       0.02,
		Execute:     ExecuteDeepAI,
	})
}

func ExecuteDeepAI(ctx *ContextBot) error {
	prompt := strings.TrimSpace(ctx.Args)

	// Flag --sticker / -s → hasil dikirim sebagai stiker.
	asSticker := false
	reSticker := regexp.MustCompile(`(?i)(^|\s)(--sticker|--stiker|-s)(\s|$)`)
	if reSticker.MatchString(prompt) {
		asSticker = true
		prompt = strings.TrimSpace(reSticker.ReplaceAllString(prompt, " "))
	}

	if prompt == "" {
		return ctx.Reply("⚠️ Sertakan instruksi edit.\n\nContoh:\n• reply gambar lalu `deepai ubah jadi gaya anime`\n• `deepai --sticker buat latar transparan`")
	}

	// Ambil gambar/stiker dari pesan yang di-reply (utamakan) atau pesan ini.
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	data, ok := src.DownloadImageFrom(ctx, quoted)
	if !ok {
		data, ok = src.DownloadImageFrom(ctx, ctx.Msg.Message)
	}
	if !ok || len(data) == 0 {
		return ctx.Reply("❌ Kirim/Balas *gambar* atau *stiker* dengan perintah `deepai <prompt>`.")
	}

	_ = ctx.React("⏳")

	outURL, err := src.DeepAI(data, "image.jpg", prompt)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memproses DeepAI: " + err.Error())
	}

	out, _, err := src.DownloadBytes(outURL)
	if err != nil || len(out) == 0 {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengunduh hasil dari DeepAI.")
	}

	if asSticker {
		if err := deepaiSendSticker(ctx, out); err != nil {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Gagal mengirim hasil sebagai stiker.")
		}
		_ = ctx.React("✅")
		return nil
	}

	if err := src.SendImageBytes(ctx, out, "image/jpeg", "✨ Hasil DeepAI"); err != nil {
		_ = ctx.React("❌")
		return err
	}
	_ = ctx.React("✅")
	return nil
}

// deepaiSendSticker mengubah buffer gambar hasil DeepAI menjadi stiker WebP
// (via API tools sticker) lalu mengirimnya.
func deepaiSendSticker(ctx *ContextBot, img []byte) error {
	webp, ctype, err := src.MakeSticker(img, "image.jpg", ctx.PushName, "DeepAI")
	if err != nil || len(webp) == 0 || strings.Contains(ctype, "application/json") {
		return err
	}
	up, err := ctx.Client.Upload(context.Background(), webp, whatsmeow.MediaImage)
	if err != nil {
		return err
	}
	msg := &waProto.Message{StickerMessage: &waProto.StickerMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		Mimetype: proto.String("image/webp"), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
		FileLength: proto.Uint64(up.FileLength),
	}}
	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msg, src.AndroidExtra())
	return err
}
