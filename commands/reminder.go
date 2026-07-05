package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src"
)

// Command `reminder` / `alarm` (owner-only) — pengingat berbasis PANGGILAN.
// Saat waktunya tiba, bot MENELEPON owner (dering = alarm) + kirim catatan teks.
//
// Pemakaian:
//   reminder <waktu> <pesan>            → pengingat sekali (mis. `reminder 10m minum obat`)
//   reminder harian <jj:mm> <pesan>     → pengingat berulang tiap hari
//   reminder list                        → daftar pengingat aktif
//   reminder del <id>                    → hapus pengingat
//   reminder clear                       → hapus semua
//
// Format <waktu>:
//   • durasi relatif: 30s, 10m, 1h, 1h30m, 2h15m
//   • jam absolut  : 07:30 (hari ini bila masih akan datang, jika lewat → besok)
//
// Opsional putar audio saat diangkat: tambahkan `| <path.mp3>` di akhir pesan,
// mis. `reminder 07:00 waktunya sholat | tmp/azan.mp3`.
func init() {
	RegisterCommand(Command{
		Name:        "Reminder",
		Category:    "Owner",
		Aliases:     []string{"reminder", "alarm", "alaram", "ingatkan"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:reminder|alarm|alaram|ingatkan)\b(.*)$`),
		Description: "[Owner] Pengingat/alarm yang MENELEPON owner saat waktunya tiba",
		Execute:     ExecuteReminder,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteReminder(ctx *ContextBot) error {
	args := strings.TrimSpace(ctx.Args)
	fields := strings.Fields(args)

	if len(fields) == 0 {
		return ctx.Reply(reminderHelp())
	}

	sub := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))

	switch sub {
	case "list", "ls", "daftar":
		return reminderList(ctx)
	case "del", "hapus", "delete", "rm", "off":
		return reminderDel(ctx, rest)
	case "clear", "hapussemua", "reset":
		n := src.ClearReminders()
		return ctx.Reply(fmt.Sprintf("🗑️ %d pengingat dihapus.", n))
	case "harian", "daily":
		return reminderAdd(ctx, rest, true)
	case "add", "set", "tambah":
		return reminderAdd(ctx, rest, false)
	default:
		// Bentuk cepat: `reminder <waktu> <pesan>`
		return reminderAdd(ctx, args, false)
	}
}

// reminderAdd mengurai "<waktu> <pesan> [| audio]" lalu mendaftarkan pengingat.
func reminderAdd(ctx *ContextBot, input string, daily bool) error {
	input = strings.TrimSpace(input)
	fields := strings.Fields(input)
	if len(fields) < 2 {
		return ctx.Reply("⚠️ Format kurang lengkap.\n\n" + reminderHelp())
	}

	whenTok := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(input, whenTok))

	// Pisahkan audio opsional setelah tanda `|`.
	message := rest
	audio := ""
	if i := strings.Index(rest, "|"); i >= 0 {
		message = strings.TrimSpace(rest[:i])
		audio = strings.TrimSpace(rest[i+1:])
	}
	if message == "" {
		return ctx.Reply("⚠️ Isi pesan pengingat kosong.")
	}

	fireAt, isClock, err := parseReminderWhen(whenTok)
	if err != nil {
		return ctx.Reply("⚠️ Waktu tidak valid: " + err.Error() + "\n\nContoh: `10m`, `1h30m`, atau `07:30`.")
	}

	// Pengingat harian wajib pakai jam absolut (jj:mm).
	if daily && !isClock {
		return ctx.Reply("⚠️ Pengingat harian harus memakai jam absolut, mis. `reminder harian 07:30 bangun`.")
	}

	r := src.AddReminder(fireAt, message, audio, daily)

	kind := "sekali"
	if daily {
		kind = "harian (tiap hari)"
	}
	when := formatWhen(r.FireAt, daily)
	reply := fmt.Sprintf(
		"⏰ *Pengingat #%d tersimpan* (%s)\n\n🕒 %s\n💬 %s",
		r.ID, kind, when, message,
	)
	if audio != "" {
		reply += "\n🎵 Audio: " + audio
	}
	reply += "\n\n_Bot akan MENELEPON Anda saat waktunya tiba._"
	return ctx.Reply(reply)
}

func reminderDel(ctx *ContextBot, arg string) error {
	arg = strings.TrimSpace(arg)
	id, err := strconv.Atoi(arg)
	if err != nil {
		return ctx.Reply("⚠️ ID tidak valid. Contoh: `reminder del 3` (lihat `reminder list`).")
	}
	if src.DelReminder(id) {
		return ctx.Reply(fmt.Sprintf("🗑️ Pengingat #%d dihapus.", id))
	}
	return ctx.Reply(fmt.Sprintf("❔ Pengingat #%d tidak ditemukan.", id))
}

func reminderList(ctx *ContextBot) error {
	list := src.ListReminders()
	if len(list) == 0 {
		return ctx.Reply("📭 Belum ada pengingat.\n\n" + reminderHelp())
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("⏰ *Daftar Pengingat* (%d)\n", len(list)))
	now := time.Now()
	for _, r := range list {
		b.WriteString("\n")
		if r.Daily {
			b.WriteString(fmt.Sprintf("*#%d* 🔁 harian %s\n", r.ID, r.FireAt.Format("15:04")))
		} else {
			sisa := r.FireAt.Sub(now).Round(time.Minute)
			b.WriteString(fmt.Sprintf("*#%d* 🕒 %s (≈ %s lagi)\n", r.ID, r.FireAt.Format("02 Jan 15:04"), src.HumanizeDur(sisa)))
		}
		b.WriteString("   💬 " + r.Message)
		if r.Audio != "" {
			b.WriteString("\n   🎵 " + r.Audio)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n`reminder del <id>` untuk menghapus.")
	return ctx.Reply(b.String())
}

// parseReminderWhen mengurai token waktu menjadi waktu picu absolut.
// Mengembalikan (waktu, isClock, error). isClock=true bila token berformat jj:mm.
func parseReminderWhen(tok string) (time.Time, bool, error) {
	now := time.Now()

	// Format jam absolut jj:mm (mis. 07:30 / 7:5).
	if strings.Contains(tok, ":") {
		parts := strings.SplitN(tok, ":", 2)
		h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return time.Time{}, false, fmt.Errorf("jam harus 00:00–23:59")
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour) // sudah lewat hari ini → besok
		}
		return t, true, nil
	}

	// Durasi relatif (10m, 1h30m, 30s, ...).
	d, err := time.ParseDuration(strings.ToLower(tok))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("gunakan durasi (10m, 1h30m) atau jam (07:30)")
	}
	if d <= 0 {
		return time.Time{}, false, fmt.Errorf("durasi harus > 0")
	}
	return now.Add(d), false, nil
}

func formatWhen(t time.Time, daily bool) string {
	if daily {
		return "tiap hari pukul " + t.Format("15:04")
	}
	return t.Format("Mon, 02 Jan 2006 15:04") + fmt.Sprintf(" (≈ %s lagi)", src.HumanizeDur(time.Until(t).Round(time.Minute)))
}

func reminderHelp() string {
	return "⏰ *Reminder / Alarm (via panggilan)*\n\n" +
		"• `reminder <waktu> <pesan>` — pengingat sekali\n" +
		"• `reminder harian <jj:mm> <pesan>` — tiap hari\n" +
		"• `reminder list` — daftar\n" +
		"• `reminder del <id>` — hapus\n" +
		"• `reminder clear` — hapus semua\n\n" +
		"*Waktu:* durasi (`10m`, `1h30m`) atau jam (`07:30`).\n" +
		"*Audio (opsional):* akhiri dengan `| path.mp3` untuk diputar saat diangkat.\n\n" +
		"Contoh:\n" +
		"• `reminder 15m cek oven`\n" +
		"• `reminder 07:00 bangun sholat | tmp/azan.mp3`\n" +
		"• `reminder harian 22:00 waktunya tidur`\n\n" +
		"_Saat waktunya tiba, bot MENELEPON Anda sebagai alarm._"
}
