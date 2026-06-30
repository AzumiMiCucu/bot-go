package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// LENS / CARI GAMBAR — pencarian gambar terbalik (reverse image search)
// via TinEye. Kirim/balas GAMBAR atau STIKER dengan perintah `lens`.
// Menampilkan domain & tautan sumber tempat gambar itu muncul di web.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Reverse Image Search",
		Category:    "Tools",
		Aliases:     []string{"lens", "tineye"},
		Pattern:     regexp.MustCompile(`(?i)^(?:lens|carigambar|tineye|sauce)\s*$`),
		Description: "Cari sumber/asal gambar di web (reply gambar/stiker dengan `lens`)",
		Price:       0.02,
		Execute:     ExecuteLens,
	})
}

func ExecuteLens(ctx *ContextBot) error {
	// Ambil gambar/stiker dari pesan yang di-reply (utamakan) atau pesan ini.
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	data, ok := src.DownloadImageFrom(ctx, quoted)
	if !ok {
		data, ok = src.DownloadImageFrom(ctx, ctx.Msg.Message)
	}
	if !ok || len(data) == 0 {
		return ctx.Reply("⚠️ Kirim/Balas *gambar* atau *stiker* dengan perintah *lens* untuk mencari sumbernya.")
	}

	go func() { _ = ctx.React("🔍") }()
	res, err := src.TinEyeSearch(data, "image.jpg")
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mencari gambar: " + err.Error())
	}

	if res == nil || len(res.Matches) == 0 {
		_ = ctx.React("🔍")
		return ctx.Reply("🔍 Tidak ada kecocokan ditemukan untuk gambar ini.")
	}

	const maxShow = 6
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 *Hasil Pencarian Gambar (TinEye)*\n")
	fmt.Fprintf(&b, "Total kecocokan: *%d*\n", res.NumMatches)

	shown := 0
	for _, m := range res.Matches {
		if shown >= maxShow {
			break
		}
		// Ambil tautan sumber terbaik dari backlink pertama.
		var srcURL, crawl string
		if len(m.Backlinks) > 0 {
			srcURL = m.Backlinks[0].URL
			crawl = m.Backlinks[0].CrawlDate
		}
		domain := m.Domain
		if domain == "" {
			domain = "(tidak diketahui)"
		}

		shown++
		fmt.Fprintf(&b, "\n*%d. %s*\n", shown, domain)
		if m.Width > 0 && m.Height > 0 {
			fmt.Fprintf(&b, "   • Ukuran: %dx%d\n", m.Width, m.Height)
		}
		if srcURL != "" {
			fmt.Fprintf(&b, "   • Sumber: %s\n", srcURL)
		}
		if crawl != "" {
			fmt.Fprintf(&b, "   • Dirayapi: %s\n", crawl)
		}
	}

	if res.NumMatches > shown {
		fmt.Fprintf(&b, "\n_…dan %d kecocokan lainnya._", res.NumMatches-shown)
	}

	_ = ctx.React("✅")
	return ctx.Reply(b.String())
}
