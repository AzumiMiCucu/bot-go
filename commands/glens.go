package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// GLENS — Google Lens reverse image search (reply gambar/stiker `glens`).
// Menampilkan judul, sumber (domain), dan tautan tempat gambar itu muncul.
// Butuh cookie Google `NID` valid (owner set via `setnid`).
//
// SETNID (owner) — simpan/perbarui cookie NID Google ke config.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Google Lens",
		Category:    "Tools",
		Aliases:     []string{"glens", "googlelens"},
		Pattern:     regexp.MustCompile(`(?i)^(?:glens|googlelens)\s*$`),
		Description: "Cari sumber gambar via Google Lens (reply gambar/stiker dengan `glens`)",
		Price:       0.02,
		Execute:     ExecuteGLens,
	})

	RegisterCommand(Command{
		Name:        "Set Google NID",
		Category:    "Owner",
		Aliases:     []string{"setnid"},
		Pattern:     regexp.MustCompile(`(?is)^setnid(?:\s+([\s\S]+))?$`),
		Description: "[Owner] Set cookie Google NID untuk fitur glens",
		Execute:     ExecuteSetNID,
	})
}

func ExecuteGLens(ctx *ContextBot) error {
	// Ambil gambar/stiker dari pesan yang di-reply (utamakan) atau pesan ini.
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	data, ok := src.DownloadImageFrom(ctx, quoted)
	if !ok {
		data, ok = src.DownloadImageFrom(ctx, ctx.Msg.Message)
	}
	if !ok || len(data) == 0 {
		return ctx.Reply("⚠️ Kirim/Balas *gambar* atau *stiker* dengan perintah *glens*.")
	}

	go func() { _ = ctx.React("🔍") }()
	results, err := src.GoogleLensSearch(data)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal Google Lens: " + err.Error())
	}
	if len(results) == 0 {
		_ = ctx.React("🔍")
		return ctx.Reply("🔍 Tidak ada hasil Google Lens untuk gambar ini.")
	}

	const maxShow = 8
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 *Google Lens*\nDitemukan *%d* hasil:\n", len(results))
	shown := 0
	for _, r := range results {
		if shown >= maxShow {
			break
		}
		shown++
		title := r.Title
		if title == "" {
			title = "(tanpa judul)"
		}
		fmt.Fprintf(&b, "\n*%d. %s*\n", shown, title)
		if r.Domain != "" {
			fmt.Fprintf(&b, "   • Sumber: %s\n", r.Domain)
		}
		fmt.Fprintf(&b, "   • %s\n", r.Link)
	}
	if len(results) > shown {
		fmt.Fprintf(&b, "\n_…dan %d hasil lainnya._", len(results)-shown)
	}

	_ = ctx.React("✅")
	return ctx.Reply(b.String())
}

func ExecuteSetNID(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa memakai setnid.")
	}
	val := strings.TrimSpace(ctx.Args)
	// Terima input berupa cookie mentah "NID=xxxx" atau nilai NID saja.
	if val == "" {
		return ctx.Reply("Gunakan: `setnid <cookie NID>`\n\nAmbil dari browser: buka lens.google.com (login), lalu salin nilai cookie *NID*. Bisa tempel `NID=...` atau nilainya saja.")
	}
	if i := strings.Index(strings.ToUpper(val), "NID="); i >= 0 {
		val = val[i+4:]
	}
	// Potong bila ada cookie lain menyusul (dipisah ; atau spasi).
	val = strings.TrimSpace(val)
	if j := strings.IndexAny(val, "; \n\r\t"); j >= 0 {
		val = val[:j]
	}
	if len(val) < 20 {
		return ctx.Reply("❌ Nilai NID terlihat tidak valid (terlalu pendek).")
	}
	src.AppConfig.GoogleNID = val
	if err := src.SaveConfig(); err != nil {
		return ctx.Reply("❌ Gagal menyimpan config: " + err.Error())
	}
	return ctx.Reply(fmt.Sprintf("✅ Cookie Google NID disimpan (%d karakter). Fitur `glens` siap dipakai.", len(val)))
}
