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
//	ctx.Print(results)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal Google Lens: " + err.Error())
	}
	if len(results) == 0 {
		_ = ctx.React("🔍")
		return ctx.Reply("🔍 Tidak ada hasil Google Lens untuk gambar ini.")
	}

	// Preview gambar BESAR: TIDAK dipaksa dari hasil teratas. Cari hasil PERTAMA
	// yang thumbnail-nya benar-benar bisa didekode (base64 → fallback URL gstatic),
	// lalu PINDAHKAN ke posisi #1 agar preview selalu tampil & nomor #1 = gambar itu.
	var thumb []byte
	for i, r := range results {
		var img []byte
		for _, t := range []string{r.Thumbnail, r.ThumbnailURL} {
			if data, _, ok := src.GLensThumbBytes(t); ok && len(data) > 0 {
				img = data
				break
			}
		}
		if img != nil {
			thumb = img
			if i != 0 {
				// Naikkan hasil ke-i menjadi #1 (geser sisanya, jaga urutan relatif).
				picked := results[i]
				results = append(results[:i], results[i+1:]...)
				results = append([]src.GLensResult{picked}, results...)
			}
			break
		}
	}

	// Susun daftar TEKS semua hasil (tanpa batas 10).
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 *Google Lens — Sumber Gambar*\n\n")
	for i, r := range results {
		title := r.Title
		if title == "" {
			title = "(tanpa judul)"
		}
		fmt.Fprintf(&b, "\n*%d.* %s\n", i+1, title)
		if r.Domain != "" {
			fmt.Fprintf(&b, "   🌐 %s\n", r.Domain)
		}
		if r.Source != "" {
			fmt.Fprintf(&b, "   🔗 %s\n", r.Source)
		}
	}
	fmt.Fprintf(&b, "\n_Total %d hasil • Balas NOMOR untuk foto + detail lengkap._", len(results))

	top := results[0]
	topTitle := top.Title
	if topTitle == "" {
		topTitle = "Google Lens"
	}

	msgID, err := src.SendTextWithThumbnailID(ctx, b.String(), top.Source, "Google Lens", topTitle, thumb)
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

	// Coba kirim foto thumbnail + detail sebagai caption. Utamakan data URI base64
	// (kualitas penuh); fallback ke URL gstatic publik. GLensThumbBytes menangani
	// keduanya (dekode base64 / unduh http).
	for _, t := range []string{r.Thumbnail, r.ThumbnailURL} {
		if img, ctype, ok := src.GLensThumbBytes(t); ok {
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
