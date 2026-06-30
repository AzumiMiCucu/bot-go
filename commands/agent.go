package commands

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"
)

// =================================================================
// AGEN AI (owner only) — antarmuka NATURAL: ketik biasa, bot memilih aksi.
// =================================================================
// Gemini bertindak sebagai ROUTER: pesan natural → satu perintah bot yang tepat
// (jadwal/tugas/email/catat/nilai/drive/tanya/copilot). Perintah hasilnya
// dijalankan lewat pipeline yang sudah ada (reuse penuh, seperti Pintasan).
//
//   - Eksplisit  : `asisten <pesan>` / `agen <pesan>` (juga jalan di grup).
//   - Otomatis   : di JAPRI owner, SEMUA pesan non-command → agen (lihat
//                  HandleOwnerAgent yang dipanggil handler.go).
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Asisten (Agen AI)",
		Category:    "Owner",
		Aliases:     []string{"asisten", "agen", "asis"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:asisten|agen|asis)\s+([\s\S]+)$`),
		Description: "[Owner] Asisten cerdas: ketik natural, bot pilih aksi otomatis",
		Execute: func(ctx *ContextBot) error {
			return runAgent(ctx, strings.TrimSpace(ctx.Args))
		},
	}).Use(OwnerOnlyMiddleware)

	// Toggle auto-agen AI di JAPRI owner. Tidak memengaruhi command eksplisit
	// `asisten`/`tanya`/`gpt`/`copilot` — hanya jawaban-otomatis pesan natural.
	RegisterCommand(Command{
		Name:        "Auto Asisten AI",
		Category:    "Owner",
		Aliases:     []string{ "autoai"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:aiauto|asistenauto|agenauto|autoai)\s*(on|off)?\s*$`),
		Description: "[Owner] Nyalakan/matikan auto-jawab AI di japri (aiauto on|off)",
		Execute:     ExecuteAIAuto,
	}).Use(OwnerOnlyMiddleware)
}

// ExecuteAIAuto menyalakan/mematikan auto-agen AI untuk japri owner.
func ExecuteAIAuto(ctx *ContextBot) error {
	arg := strings.ToLower(strings.TrimSpace(ctx.Args))

	if arg == "" {
		status := "OFF"
		if src.IsAutoAgentAI() {
			status = "ON"
		}
		return ctx.Reply(fmt.Sprintf("🤖 *Auto-Jawab AI (japri):* %s\n\nGunakan `aiauto on` / `aiauto off`.\nSaat OFF, pesanmu di japri tidak akan dijawab AI otomatis — kamu tetap bisa pakai `asisten <pesan>` / `tanya <pesan>` secara manual.", status))
	}

	switch arg {
	case "on":
		src.SetAutoAgentAI(true)
		return ctx.Reply("✅ *Auto-Jawab AI AKTIF.* Pesan natural di japri akan dijawab agen otomatis.")
	case "off":
		src.SetAutoAgentAI(false)
		return ctx.Reply("🔕 *Auto-Jawab AI NONAKTIF.* Pesanmu di japri tidak akan dijawab AI. Pakai `asisten <pesan>` bila butuh.")
	default:
		return ctx.Reply("⚠️ Pilihan tidak valid. Gunakan `aiauto on` atau `aiauto off`.")
	}
}

// HandleOwnerAgent dipanggil handler.go untuk JAPRI owner: setiap pesan natural
// (bukan command) dialihkan ke agen. Dijalankan asinkron agar tak memblokir.
// Mengembalikan true bila pesan dikonsumsi.
//
// PENTING: hanya pesan ber-TEKS yang dialihkan ke agen. Media polos tanpa teks
// (mis. stiker/gambar yang dikirim begitu saja) TIDAK diproses otomatis — kalau
// tidak, SETIAP stiker yang owner kirim akan ikut dianalisa vision Copilot. Untuk
// menanyai sebuah stiker/gambar, owner memakai perintah eksplisit: reply media lalu
// `copilot <pertanyaan>` (vision via runCopilot). Gambar BER-CAPTION tetap jalan
// karena caption mengisi TextMessage.
func HandleOwnerAgent(ctx *ContextBot) bool {
	// Toggle owner: bila auto-agen dimatikan, biarkan pesan owner lewat tanpa
	// dijawab AI (owner bisa mengetik bebas / pakai command eksplisit `asisten`).
	if !src.IsAutoAgentAI() {
		return false
	}
	if strings.TrimSpace(ctx.TextMessage) == "" {
		return false
	}
	go func() { _ = runAgent(ctx, strings.TrimSpace(ctx.TextMessage)) }()
	return true
}

func runAgent(ctx *ContextBot, message string) error {
	if message == "" && aiHasImage(ctx) {
		message = "Jelaskan gambar ini."
	}
	if message == "" {
		return ctx.Reply("🧠 Ada yang bisa dibantu? Contoh: _\"jadwalku besok apa\"_, _\"ingetin tugas laporan jumat\"_, _\"ringkas email\"_, _\"jelasin efek doppler\"_.")
	}
	_ = ctx.React("🧠")
	cmdline, err := planAgent(ctx, message)
	if err != nil || strings.TrimSpace(cmdline) == "" {
		// Fallback: perlakukan sebagai pertanyaan umum.
		cmdline = "tanya " + message
	}
	return dispatchAgentCommand(ctx, cmdline)
}

// agentCommandCatalog = daftar perintah yang boleh dipilih router (sengaja TIDAK
// memuat perintah berbahaya seperti eval/broadcast).
const agentCommandCatalog = `- "jadwal" → lihat agenda terdekat
- "jadwal tambah <judul> | <YYYY-MM-DD HH:MM> | <durasi_menit>" → buat acara kalender
- "tugas" → lihat daftar tugas
- "tugas tambah <judul> | <YYYY-MM-DD>" → tambah tugas (tanggal opsional)
- "tugas selesai <nomor>" → tandai tugas selesai
- "email" → lihat email belum dibaca
- "email ringkas" → ringkas email belum dibaca
- "catat <judul> | <isi>" → buat catatan di Google Docs
- "nilai" → lihat tracker nilai kuliah
- "nilai tambah <matkul> | <sks> | <nilai>" → catat nilai
- "drive list" → lihat file Google Drive terbaru
- "tanya <pertanyaan>" → pertanyaan umum / penjelasan materi (fisika, matematika, dll)`

// planAgent meminta Gemini menerjemahkan pesan natural menjadi satu perintah bot.
func planAgent(ctx *ContextBot, message string) (string, error) {
	now := time.Now().In(jakartaLoc)
	imgNote := ""
	catalog := agentCommandCatalog
	if aiHasImage(ctx) {
		imgNote = "\nPESAN INI MENYERTAKAN GAMBAR. Untuk apa pun yang berkaitan dengan gambar gunakan: \"copilot <pertanyaan tentang gambar>\"."
		catalog += "\n- \"copilot <pertanyaan>\" → analisa/tanya tentang GAMBAR yang dikirim (vision)"
	}

	prompt := "Kamu router asisten untuk bot WhatsApp milik mahasiswa fisika. " +
		"Tanggal & waktu sekarang: " + now.Format("Monday, 02 January 2006 15:04") + " WIB.\n" +
		"Ubah pesan pengguna menjadi SATU perintah bot paling tepat. " +
		"Balas HANYA satu objek JSON valid tanpa penjelasan/markdown, format persis: {\"cmd\":\"<perintah lengkap>\"}\n\n" +
		"Daftar perintah yang tersedia:\n" + catalog + imgNote + "\n\n" +
		"Aturan:\n" +
		"- Tanggal relatif (hari ini, besok, lusa, senin depan) hitung jadi YYYY-MM-DD dari tanggal sekarang.\n" +
		"- Acara tanpa jam → pakai jam wajar (08:00) dan durasi 60.\n" +
		"- Obrolan biasa atau pertanyaan pengetahuan → gunakan \"tanya <pesan asli pengguna>\".\n" +
		"- Jangan mengarang perintah di luar daftar.\n\n" +
		"Contoh:\n" +
		"pesan: \"ingetin besok praktikum fisika jam 1 siang 2 jam\" → {\"cmd\":\"jadwal tambah Praktikum Fisika | " + now.AddDate(0, 0, 1).Format("2006-01-02") + " 13:00 | 120\"}\n" +
		"pesan: \"tugas apa aja minggu ini?\" → {\"cmd\":\"tugas\"}\n" +
		"pesan: \"catat rumus GLBB: v=v0+at\" → {\"cmd\":\"catat Rumus GLBB | v=v0+at\"}\n" +
		"pesan: \"ada email penting ga\" → {\"cmd\":\"email\"}\n" +
		"pesan: \"jelasin efek doppler dong\" → {\"cmd\":\"tanya jelasin efek doppler\"}\n\n" +
		"Pesan pengguna: \"" + strings.ReplaceAll(message, "\"", "'") + "\""

	raw, _, err := src.GeminiChat(prompt, "Balas hanya JSON valid satu baris.", "")
	if err != nil {
		return "", err
	}
	return parseAgentCmd(raw)
}

func parseAgentCmd(raw string) (string, error) {
	s := raw
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j >= i {
			s = s[i : j+1]
		}
	}
	var r struct {
		Cmd   string `json:"cmd"`
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return "", err
	}
	cmd := strings.TrimSpace(r.Cmd)
	if cmd == "" && r.Reply != "" {
		cmd = "tanya " + r.Reply
	}
	if cmd == "" {
		return "", fmt.Errorf("cmd kosong")
	}
	return cmd, nil
}

// dispatchAgentCommand menjalankan perintah hasil router lewat pipeline command.
func dispatchAgentCommand(ctx *ContextBot, cmdline string) error {
	cmdline = strings.TrimLeft(strings.TrimSpace(cmdline), "/.!#")
	cmd, args := MatchCommand(cmdline)
	if cmd == nil {
		// Bukan command dikenal → jawab sebagai pertanyaan umum.
		cmd, args = MatchCommand("tanya " + cmdline)
		if cmd == nil {
			return ctx.Reply("🤔 Maaf, saya belum paham maksudnya. Coba ulangi dengan kalimat lain.")
		}
	}
	ctx.Args = args
	ctx.TextMessage = cmdline
	ctx.Print("[AGEN] → %s", cmdline)
	return ExecuteWithMiddlewares(ctx, cmd)
}
