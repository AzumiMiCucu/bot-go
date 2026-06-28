package commands

import (
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// GOOGLE AUTH — command otorisasi Google Workspace (owner only).
//
//	gauth              → tampilkan URL consent + instruksi
//	gauth code <code>  → tukar kode → simpan token
//	gauth status       → cek status koneksi
//	gauth logout       → hapus token (putus koneksi)
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Google Auth",
		Category:    "Owner",
		Aliases:     []string{"gauth", "googleauth"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:gauth|googleauth)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Hubungkan akun Google (Calendar/Tasks/Gmail/Drive/Docs/Sheets)",
		Execute:     ExecuteGAuth,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteGAuth(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	sub := strings.ToLower(arg)

	switch {
	case sub == "status":
		if src.GoogleReady() {
			return ctx.Reply("✅ Google: *terhubung*. Token tersimpan dan siap dipakai.")
		}
		return ctx.Reply("⚠️ Google: *belum terhubung*. Jalankan `gauth` untuk mulai.")

	case sub == "logout" || sub == "disconnect":
		if err := src.GoogleLogout(); err != nil {
			return ctx.Reply("⚠️ Tidak ada token untuk dihapus (atau gagal): " + err.Error())
		}
		return ctx.Reply("🔌 Token Google dihapus. Jalankan `gauth` untuk menghubungkan lagi.")

	case strings.HasPrefix(sub, "code"):
		code := strings.TrimSpace(arg[len("code"):])
		// Toleran: user mungkin menempel seluruh URL redirect, ambil parameter code-nya.
		if i := strings.Index(code, "code="); i >= 0 {
			code = code[i+5:]
			if amp := strings.IndexAny(code, "&# "); amp >= 0 {
				code = code[:amp]
			}
		}
		code = strings.TrimSpace(code)
		if code == "" {
			return ctx.Reply("⚠️ Format: `gauth code <kode>` (kode dari URL redirect setelah approve).")
		}
		_ = ctx.React("⏳")
		if err := src.GoogleExchange(code); err != nil {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Gagal menukar kode: " + err.Error())
		}
		_ = ctx.React("✅")
		return ctx.Reply("✅ *Akun Google terhubung!*\n\nLayanan siap: Calendar, Tasks (Gmail/Drive/Docs/Sheets menyusul).\nCoba: `jadwal` atau `tugas`.")

	default:
		url, err := src.GoogleAuthURL()
		if err != nil {
			return ctx.Reply("❌ " + err.Error() + "\n\n_Unggah dulu `credentials.json` ke folder `src/secret/` (lihat panduan)._")
		}
		return ctx.Reply("🔐 *HUBUNGKAN GOOGLE*\n\n" +
			"1. Buka link ini di browser & login akun Google-mu:\n" + url + "\n\n" +
			"2. Setujui izinnya. Browser akan diarahkan ke `http://localhost/?code=...` " +
			"(halaman gagal dimuat itu *normal*).\n\n" +
			"3. Salin nilai `code` dari address bar, lalu kirim:\n" +
			"`gauth code <kode>`\n\n" +
			"_Tips: boleh tempel seluruh URL-nya, nanti saya ambil kodenya otomatis._")
	}
}
