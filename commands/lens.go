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

	// Susun daftar TEKS semua kecocokan (tanpa batas 10).
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 *TinEye — Sumber Gambar*\n\n")
	for i, m := range matches {
		domain := m.Domain
		if domain == "" {
			domain = "(tidak diketahui)"
		}
		page, _ := lensMatchURLs(m)
		line := fmt.Sprintf("\n*%d.* %s", i+1, domain)
		if m.Width > 0 && m.Height > 0 {
			line += fmt.Sprintf(" • %dx%d", m.Width, m.Height)
		}
		b.WriteString(line + "\n")
		if page != "" {
			fmt.Fprintf(&b, "   🔗 %s\n", page)
		}
	}
	fmt.Fprintf(&b, "\n_Total %d kecocokan • Balas NOMOR untuk foto + detail._", res.NumMatches)

	// Preview gambar BESAR = gambar kecocokan TERATAS.
	top := matches[0]
	topPage, topImage := lensMatchURLs(top)
	var thumb []byte
	if topImage != "" {
		if img, _, derr := src.DownloadBytes(topImage); derr == nil && len(img) > 0 {
			thumb = img
		}
	}
	topDomain := top.Domain
	if topDomain == "" {
		topDomain = "TinEye"
	}

	msgID, err := src.SendTextWithThumbnailID(ctx, b.String(), topPage, "TinEye", topDomain, thumb)
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
