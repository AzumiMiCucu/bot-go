package commands

import (
	"fmt"
	"regexp"

	"bot-go/src"
)

// =================================================================
// MEDIAFIRE DOWNLOADER — resolve tautan via ps.azumi.dev lalu kirim file
// sebagai dokumen (lihat downloader_util.go untuk sendFileDocument).
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "MediaFire Downloader",
		Category:    "Downloader",
		Aliases:     []string{"mediafiredl", "mediafire", "mfdl"},
		Pattern:     regexp.MustCompile(`(?i)^(?:mediafiredl|mediafire|mfdl)\s+(.+)`),
		Description: "Unduh file dari MediaFire",
		Execute:     ExecuteMediafire,
	})
}

var reMediafireURL = regexp.MustCompile(`(?i)(https?://(?:www\.)?mediafire\.com/[^\s]+)`)

func ExecuteMediafire(ctx *ContextBot) error {
	m := reMediafireURL.FindStringSubmatch(ctx.TextMessage)
	if len(m) == 0 {
		return ctx.Reply("⚠️ URL MediaFire tidak valid.\n\nContoh: `mfdl https://www.mediafire.com/file/xxxx/file`")
	}
	_ = ctx.React("⏳")

	res, err := src.MediafireDL(m[1])
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal: " + err.Error())
	}

	caption := fmt.Sprintf("📦 *MediaFire*\n📄 %s\n📐 %s • %s", res.Filename, res.FilesizeH, res.Filetype)
	if err := sendFileDocument(ctx, res.Download, res.Filename, "", caption); err != nil {
		_ = ctx.React("❌")
		return ctx.Reply(err.Error())
	}
	_ = ctx.React("✅")
	return nil
}
