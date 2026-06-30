package commands

import (
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// HD / UPSCALE — tingkatkan resolusi & kualitas gambar via waifu2x.pro.
// Kirim/balas GAMBAR atau STIKER dengan perintah `hd`.
//
//	hd            → mode ANIME, output JPEG (default)
//	hd foto       → mode PHOTO (cocok untuk foto asli)
//	hd png        → output PNG (berkas lebih besar, tanpa kompresi lossy)
//	hd foto png   → gabungan
//
// Flag bisa digabung & bebas urutan.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Upscale HD",
		Category:    "Tools",
		Aliases:     []string{"hd", "waifu2x",},
		Pattern:     regexp.MustCompile(`(?i)^(?:hd|upscale|waifu2x)(?:\s+([\s\S]+))?$`),
		Description: "Tingkatkan kualitas/resolusi gambar (reply gambar/stiker dengan `hd`)",
		Price:       0.02,
		Execute:     ExecuteUpscale,
	})
}

func ExecuteUpscale(ctx *ContextBot) error {
	args := strings.ToLower(strings.TrimSpace(ctx.Args))

	opts := src.UpscaleOptions{}
	if regexp.MustCompile(`(?i)(^|\s)(foto|photo)(\s|$)`).MatchString(args) {
		opts.Type = "PHOTO"
	}
	outMime := "image/jpeg"
	if regexp.MustCompile(`(?i)(^|\s)png(\s|$)`).MatchString(args) {
		opts.Format = "PNG"
		outMime = "image/png"
	}

	// Ambil gambar/stiker dari pesan yang di-reply (utamakan) atau pesan ini.
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	data, ok := src.DownloadImageFrom(ctx, quoted)
	if !ok {
		data, ok = src.DownloadImageFrom(ctx, ctx.Msg.Message)
	}
	if !ok || len(data) == 0 {
		return ctx.Reply("⚠️ Kirim/Balas *gambar* atau *stiker* dengan perintah *hd*.\n\nContoh:\n• `hd` — mode anime\n• `hd foto` — mode foto asli\n• `hd png` — output PNG")
	}

	go func() { _ = ctx.React("⏳") }()
	out, err := src.UpscaleImage(data, opts)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal meningkatkan kualitas gambar: " + err.Error())
	}

	if err := src.SendImageBytes(ctx, out, outMime, "✨ Hasil HD"); err != nil {
		_ = ctx.React("❌")
		return err
	}
	_ = ctx.React("✅")
	return nil
}
