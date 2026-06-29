package commands

import (
	"fmt"
	"regexp"

	"bot-go/src"
)

// =================================================================
// MEGA DOWNLOADER — resolve tautan via ps.azumi.dev lalu kirim file sebagai
// dokumen (lihat downloader_util.go untuk sendFileDocument).
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Mega Downloader",
		Category:    "Downloader",
		Aliases:     []string{"megadl", "mega", "mdl"},
		Pattern:     regexp.MustCompile(`(?i)^(?:megadl|mega|mdl)\s+(.+)`),
		Description: "Unduh file dari Mega.nz",
		Execute:     ExecuteMega,
	})
}

var reMegaURL = regexp.MustCompile(`(?i)(https?://mega(?:\.co)?\.nz/[^\s]+)`)

func ExecuteMega(ctx *ContextBot) error {
	m := reMegaURL.FindStringSubmatch(ctx.TextMessage)
	if len(m) == 0 {
		return ctx.Reply("⚠️ URL Mega tidak valid.\n\nContoh: `mdl https://mega.nz/file/xxxx#key`")
	}
	go func() { _ = ctx.React("⏳") }()
	res, err := src.MegaDL(m[1])
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal: " + err.Error())
	}

	caption := fmt.Sprintf("📦 *Mega.nz*\n📄 %s\n📐 %s", res.FileName, res.FileSize)
	if err := sendFileDocument(ctx, res.DownloadURL, res.FileName, res.MimeType, caption); err != nil {
		_ = ctx.React("❌")
		return ctx.Reply(err.Error())
	}
	_ = ctx.React("✅")
	return nil
}
