package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"bot-go/src"
)

// =================================================================
// LENS / CARI GAMBAR — reverse image search via TinEye. Reply gambar/stiker
// dengan `lens`. Menampilkan daftar sumber bernomor (kartu AIRich bergambar);
// balas dengan NOMOR → bot mengirim FOTO + detail lengkap sumber tersebut.
// (Konsep pengiriman disamakan dengan `glens`.)
// =================================================================

// lensSession menyimpan hasil TinEye untuk interaksi reply-nomor.
type lensSession struct {
	Matches    []src.TinEyeMatch
	NumMatches int
}

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

// lensMatchURLs mengembalikan (halaman sumber, url gambar) terbaik dari sebuah
// match TinEye.
func lensMatchURLs(m src.TinEyeMatch) (page, image string) {
	if len(m.Backlinks) > 0 {
		page = m.Backlinks[0].URL
	}
	image = m.ImageURL
	if image == "" && len(m.Backlinks) > 0 {
		image = m.Backlinks[0].Backlink
	}
	return page, image
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

	matches := res.Matches
	const maxShow = 10
	if len(matches) > maxShow {
		matches = matches[:maxShow]
	}

	rb := src.NewAIRich().
		SetTitle("🔎 TinEye — Sumber Gambar").
		SetFooter(fmt.Sprintf("Total %d kecocokan • Balas NOMOR (1-%d) untuk foto + detail", res.NumMatches, len(matches)))

	for i, m := range matches {
		domain := m.Domain
		if domain == "" {
			domain = "(tidak diketahui)"
		}
		page, image := lensMatchURLs(m)
		label := domain
		if m.Width > 0 && m.Height > 0 {
			label = fmt.Sprintf("%s • %dx%d", domain, m.Width, m.Height)
		}
		rb.AddText(fmt.Sprintf("*%d.* %s", i+1, label))
		rb.AddProduct(src.AIProduct{
			Title:      domain,
			Brand:      fmt.Sprintf("%dx%d", m.Width, m.Height),
			Price:      fmt.Sprintf("#%d", i+1),
			ProductURL: page,
			ImageURL:   image,
		})
	}

	msgID, err := rb.SendToChatWithID(ctx)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal menampilkan daftar hasil.")
	}
	replyRouter.Register(msgID, "lens", &lensSession{Matches: matches, NumMatches: res.NumMatches})
	_ = ctx.React("✅")
	return nil
}

// handleLensReply menangani reply NOMOR pada daftar hasil lens: kirim foto
// sumber (bila ada) + detail lengkap. Mengembalikan true hanya bila reply
// berupa nomor valid (agar tak menelan pesan lain).
func handleLensReply(ctx *ContextBot, rc *ReplyContext) bool {
	sess, ok := rc.Data.(*lensSession)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.TextMessage))
	if err != nil || n < 1 || n > len(sess.Matches) {
		return false
	}
	m := sess.Matches[n-1]

	domain := m.Domain
	if domain == "" {
		domain = "(tidak diketahui)"
	}
	page, image := lensMatchURLs(m)

	var b strings.Builder
	fmt.Fprintf(&b, "🔎 *TinEye — Hasil #%d*\n\n🌐 *Domain:* %s\n", n, domain)
	if m.Width > 0 && m.Height > 0 {
		fmt.Fprintf(&b, "📐 *Ukuran:* %dx%d\n", m.Width, m.Height)
	}
	if page != "" {
		fmt.Fprintf(&b, "🔗 *Sumber:* %s\n", page)
	}
	if len(m.Backlinks) > 0 && m.Backlinks[0].CrawlDate != "" {
		fmt.Fprintf(&b, "🗓️ *Dirayapi:* %s\n", m.Backlinks[0].CrawlDate)
	}
	if image != "" {
		fmt.Fprintf(&b, "🖼️ *Gambar:* %s\n", image)
	}
	caption := strings.TrimRight(b.String(), "\n")

	go func() { _ = ctx.React("⏳") }()

	// Coba kirim foto gambar sumber + detail sebagai caption.
	if image != "" {
		if img, ctype, derr := src.DownloadBytes(image); derr == nil && len(img) > 0 {
			if ctype == "" {
				ctype = "image/jpeg"
			}
			if serr := src.SendImageBytes(ctx, img, ctype, caption); serr == nil {
				_ = ctx.React("✅")
				return true
			}
		}
	}

	// Fallback: tak ada gambar / gagal unduh → kirim detail teks saja.
	_ = ctx.Reply(caption)
	_ = ctx.React("✅")
	return true
}
