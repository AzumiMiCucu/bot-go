package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"bot-go/src"
)

// =================================================================
// GLENS — Google Lens reverse image search (reply gambar/stiker `glens`).
// Menampilkan daftar sumber bernomor (kartu AIRich bergambar). Balas dengan
// NOMOR → bot mengirim FOTO thumbnail + detail lengkap sumber tersebut.
// Butuh cookie Google PENUH & valid (owner set via `setnid`).
//
// SETNID (owner) — simpan/perbarui cookie Google PENUH ke config.
// =================================================================

// glensSession menyimpan hasil pencarian untuk interaksi reply-nomor.
type glensSession struct {
	Results []src.GLensResult
}

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
		Name:        "Set Google Cookie",
		Category:    "Owner",
		Aliases:     []string{"setnid", "setgcookie"},
		Pattern:     regexp.MustCompile(`(?is)^(?:setnid|setgcookie)(?:\s+([\s\S]+))?$`),
		Description: "[Owner] Set cookie Google PENUH untuk fitur glens",
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

	const maxShow = 10
	if len(results) > maxShow {
		results = results[:maxShow]
	}

	rb := src.NewAIRich().
		SetTitle("🔎 Google Lens — Sumber Gambar").
		SetFooter(fmt.Sprintf("Balas NOMOR (1-%d) untuk foto + detail lengkap", len(results)))

	for i, r := range results {
		title := r.Title
		if title == "" {
			title = "(tanpa judul)"
		}
		rb.AddText(fmt.Sprintf("*%d.* %s", i+1, title))
		rb.AddProduct(src.AIProduct{
			Title:      title,
			Brand:      r.Domain,
			Price:      fmt.Sprintf("#%d", i+1),
			ProductURL: r.Source,
			ImageURL:   r.Thumbnail,
		})
	}

	msgID, err := rb.SendToChatWithID(ctx)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal menampilkan daftar hasil.")
	}
	replyRouter.Register(msgID, "glens", &glensSession{Results: results})
	_ = ctx.React("✅")
	return nil
}

// handleGLensReply menangani reply NOMOR pada daftar hasil glens: kirim foto
// thumbnail (bila ada) + detail lengkap sumber terpilih. Mengembalikan true
// hanya bila reply berupa nomor valid (agar tak menelan pesan lain).
func handleGLensReply(ctx *ContextBot, rc *ReplyContext) bool {
	sess, ok := rc.Data.(*glensSession)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.TextMessage))
	if err != nil || n < 1 || n > len(sess.Results) {
		return false
	}
	r := sess.Results[n-1]

	title := r.Title
	if title == "" {
		title = "(tanpa judul)"
	}
	caption := fmt.Sprintf(
		"🔎 *Google Lens — Hasil #%d*\n\n📌 *Judul:* %s\n🌐 *Domain:* %s\n🔗 *Sumber:* %s",
		n, title, r.Domain, r.Source,
	)

	go func() { _ = ctx.React("⏳") }()

	// Coba kirim foto thumbnail + detail sebagai caption.
	if r.Thumbnail != "" {
		if img, ctype, derr := src.DownloadBytes(r.Thumbnail); derr == nil && len(img) > 0 {
			if ctype == "" {
				ctype = "image/jpeg"
			}
			if serr := src.SendImageBytes(ctx, img, ctype, caption); serr == nil {
				_ = ctx.React("✅")
				return true
			}
		}
	}

	// Fallback: tak ada thumbnail / gagal unduh → kirim detail teks saja.
	_ = ctx.Reply(caption)
	_ = ctx.React("✅")
	return true
}

func ExecuteSetNID(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa memakai setnid.")
	}
	val := strings.TrimSpace(ctx.Args)
	if val == "" {
		return ctx.Reply("Gunakan: `setnid <cookie penuh>`\n\nAmbil dari browser yang LOGIN google (DevTools → Network → request ke google.com → header *Cookie*), salin SELURUH nilainya (mis. `AEC=..; NID=..; __Secure-..=..`). Cookie penuh membuat hasil `glens` lengkap.")
	}
	// Simpan cookie PENUH apa adanya (hanya rapikan spasi/petik pembungkus).
	val = strings.TrimSpace(strings.Trim(val, "\"'"))
	if len(val) < 20 {
		return ctx.Reply("❌ Cookie terlihat tidak valid (terlalu pendek).")
	}
	src.AppConfig.GoogleCookie = val
	if err := src.SaveConfig(); err != nil {
		return ctx.Reply("❌ Gagal menyimpan config: " + err.Error())
	}
	hasNID := ""
	if !strings.Contains(strings.ToUpper(val), "NID=") {
		hasNID = "\n\n⚠️ Catatan: cookie tidak memuat `NID=` — pastikan kamu menyalin cookie dari sesi Google yang LOGIN agar hasil lengkap."
	}
	return ctx.Reply(fmt.Sprintf("✅ Cookie Google disimpan (%d karakter). Fitur `glens` siap dipakai.%s", len(val), hasNID))
}
