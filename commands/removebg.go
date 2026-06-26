package commands

import (
	"regexp"
	"strings"

	"bot-go/src"
)

func init() {
	RegisterCommand(Command{
		Name:        "Remove Background",
		Category:    "Tools",
		Aliases:     []string{"removebg", "rmbg", "nobg"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:removebg|rmbg|nobg)\s*$`),
		Description: "Hapus latar belakang gambar (kirim/balas gambar dengan perintah removebg)",
		Execute:     ExecuteRemoveBg,
	})
}

func ExecuteRemoveBg(ctx *ContextBot) error {
	// Ambil gambar dari pesan ini atau pesan yang di-reply (termasuk view-once).
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	var data []byte
	var ok bool
	if data, ok = src.DownloadImageFrom(ctx, quoted); !ok {
		data, ok = src.DownloadImageFrom(ctx, ctx.Msg.Message)
	}
	if !ok || len(data) == 0 {
		return ctx.Reply("❌ Kirim/Balas *gambar* (termasuk *sekali lihat*) dengan perintah *removebg*.")
	}

	ctx.Reply("⏳ Menghapus latar belakang...")

	out, ctype, err := src.RemoveBg(data, "image.jpg")
	if err != nil {
		return ctx.Reply("❌ Gagal menghapus latar belakang.")
	}
	if len(out) == 0 || strings.Contains(ctype, "application/json") {
		return ctx.Reply("❌ API removebg mengembalikan hasil tak valid.")
	}

	// Hasil PNG transparan — kirim sebagai gambar PNG agar transparansi terjaga.
	return src.SendImageBytes(ctx, out, "image/png", "")
}
