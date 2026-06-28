package commands

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/tasks/v1"
)

// =================================================================
// GOOGLE CALENDAR & TASKS (owner only) — asisten jadwal & tugas kuliah.
//
//	jadwal                       → agenda 10 acara terdekat
//	jadwal tambah <judul> | <waktu> [| <durasi menit>]
//	tugas                        → daftar tugas (belum selesai)
//	tugas tambah <judul> [| <tgl>]
//	tugas selesai <nomor>
//
// Format waktu/tanggal: "2006-01-02 15:04" atau "2006-01-02" (zona Asia/Jakarta).
// =================================================================

var jakartaLoc = mustLoadJakarta()

func mustLoadJakarta() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		return time.FixedZone("WIB", 7*3600)
	}
	return loc
}

func init() {
	RegisterCommand(Command{
		Name:        "Jadwal (Calendar)",
		Category:    "Owner",
		Aliases:     []string{"jadwal", "agenda", "calendar"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:jadwal|agenda|calendar)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Lihat/tambah acara Google Calendar",
		Execute:     ExecuteJadwal,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Tugas (Tasks)",
		Category:    "Owner",
		Aliases:     []string{"tugas", "todo", "task"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:tugas|todo|task)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Lihat/tambah/selesaikan tugas Google Tasks",
		Execute:     ExecuteTugas,
	}).Use(OwnerOnlyMiddleware)
}

// parseLocalTime mengurai waktu/tanggal lokal (Asia/Jakarta).
func parseLocalTime(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	dateOnly := []string{"2006-01-02", "02-01-2006", "02/01/2006"}
	dateTime := []string{"2006-01-02 15:04", "2006-01-02T15:04", "02-01-2006 15:04", "02/01/2006 15:04"}
	for _, f := range dateTime {
		if t, err := time.ParseInLocation(f, s, jakartaLoc); err == nil {
			return t, false, nil
		}
	}
	for _, f := range dateOnly {
		if t, err := time.ParseInLocation(f, s, jakartaLoc); err == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("format waktu tak dikenal (pakai `2006-01-02 15:04`)")
}

func calendarService(ctx *ContextBot) (*calendar.Service, error) {
	c, err := src.GoogleClient(ctx.Ctx)
	if err != nil {
		return nil, err
	}
	return calendar.NewService(ctx.Ctx, option.WithHTTPClient(c))
}

func tasksService(ctx *ContextBot) (*tasks.Service, error) {
	c, err := src.GoogleClient(ctx.Ctx)
	if err != nil {
		return nil, err
	}
	return tasks.NewService(ctx.Ctx, option.WithHTTPClient(c))
}

// ============================ JADWAL ============================

func ExecuteJadwal(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	parts := strings.SplitN(arg, " ", 2)
	sub := strings.ToLower(parts[0])
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}

	switch sub {
	case "tambah", "add", "buat":
		return jadwalAdd(ctx, rest)
	case "", "list", "lihat":
		return jadwalList(ctx)
	default:
		return jadwalList(ctx)
	}
}

func jadwalList(ctx *ContextBot) error {
	_ = ctx.React("📅")
	srv, err := calendarService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 25*time.Second)
	defer cancel()
	now := time.Now().Format(time.RFC3339)
	res, err := srv.Events.List("primary").ShowDeleted(false).SingleEvents(true).
		TimeMin(now).MaxResults(10).OrderBy("startTime").Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal ambil agenda: " + err.Error())
	}
	if len(res.Items) == 0 {
		return ctx.Reply("📭 Tidak ada acara mendatang.\n\nTambah: `jadwal tambah Kuliah Fisika | 2026-07-01 08:00`")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📅 *AGENDA TERDEKAT* (%d)\n\n", len(res.Items)))
	for i, e := range res.Items {
		when := e.Start.DateTime
		if when == "" {
			when = e.Start.Date // acara seharian
		}
		if t, err := time.Parse(time.RFC3339, when); err == nil {
			when = t.In(jakartaLoc).Format("Mon, 02 Jan 15:04")
		}
		sb.WriteString(fmt.Sprintf("%d. *%s*\n   🕒 %s\n", i+1, e.Summary, when))
	}
	return ctx.Reply(sb.String())
}

func jadwalAdd(ctx *ContextBot, rest string) error {
	if rest == "" {
		return ctx.Reply("⚠️ Format: `jadwal tambah <judul> | <waktu> [| <durasi menit>]`\n" +
			"Contoh: `jadwal tambah Praktikum Fisika | 2026-07-01 13:00 | 120`")
	}
	fields := strings.Split(rest, "|")
	title := strings.TrimSpace(fields[0])
	if title == "" || len(fields) < 2 {
		return ctx.Reply("⚠️ Sertakan judul dan waktu. Contoh: `jadwal tambah Kuliah | 2026-07-01 08:00`")
	}
	start, dateOnly, err := parseLocalTime(fields[1])
	if err != nil {
		return ctx.Reply("⚠️ " + err.Error())
	}
	durMin := 60
	if len(fields) >= 3 {
		if n, e := strconv.Atoi(strings.TrimSpace(fields[2])); e == nil && n > 0 {
			durMin = n
		}
	}

	_ = ctx.React("📅")
	srv, err := calendarService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	ev := &calendar.Event{Summary: title}
	if dateOnly {
		ev.Start = &calendar.EventDateTime{Date: start.Format("2006-01-02")}
		ev.End = &calendar.EventDateTime{Date: start.AddDate(0, 0, 1).Format("2006-01-02")}
	} else {
		end := start.Add(time.Duration(durMin) * time.Minute)
		ev.Start = &calendar.EventDateTime{DateTime: start.Format(time.RFC3339), TimeZone: "Asia/Jakarta"}
		ev.End = &calendar.EventDateTime{DateTime: end.Format(time.RFC3339), TimeZone: "Asia/Jakarta"}
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 25*time.Second)
	defer cancel()
	created, err := srv.Events.Insert("primary", ev).Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal membuat acara: " + err.Error())
	}
	return ctx.Reply(fmt.Sprintf("✅ Acara dibuat: *%s*\n🕒 %s\n🔗 %s",
		created.Summary, start.In(jakartaLoc).Format("Mon, 02 Jan 2006 15:04"), created.HtmlLink))
}

// ============================ TUGAS ============================

func ExecuteTugas(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	parts := strings.SplitN(arg, " ", 2)
	sub := strings.ToLower(parts[0])
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}

	switch sub {
	case "tambah", "add", "buat":
		return tugasAdd(ctx, rest)
	case "selesai", "done", "beres":
		return tugasDone(ctx, rest)
	case "", "list", "lihat":
		return tugasList(ctx)
	default:
		return tugasList(ctx)
	}
}

func tugasList(ctx *ContextBot) error {
	_ = ctx.React("📝")
	srv, err := tasksService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 25*time.Second)
	defer cancel()
	res, err := srv.Tasks.List("@default").ShowCompleted(false).MaxResults(50).Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal ambil tugas: " + err.Error())
	}
	if len(res.Items) == 0 {
		return ctx.Reply("🎉 Tidak ada tugas tertunda.\n\nTambah: `tugas tambah Laporan praktikum | 2026-07-05`")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📝 *TUGAS* (%d)\n\n", len(res.Items)))
	for i, t := range res.Items {
		due := ""
		if t.Due != "" {
			if d, err := time.Parse(time.RFC3339, t.Due); err == nil {
				due = " — ⏰ " + d.In(jakartaLoc).Format("02 Jan")
			}
		}
		sb.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, t.Title, due))
	}
	sb.WriteString("\n_Selesaikan: `tugas selesai <nomor>`_")
	return ctx.Reply(sb.String())
}

func tugasAdd(ctx *ContextBot, rest string) error {
	if rest == "" {
		return ctx.Reply("⚠️ Format: `tugas tambah <judul> [| <tgl>]`\nContoh: `tugas tambah Laporan praktikum | 2026-07-05`")
	}
	fields := strings.SplitN(rest, "|", 2)
	title := strings.TrimSpace(fields[0])
	if title == "" {
		return ctx.Reply("⚠️ Judul tugas kosong.")
	}
	task := &tasks.Task{Title: title}
	if len(fields) == 2 {
		if d, _, err := parseLocalTime(fields[1]); err == nil {
			// Google Tasks `due` hanya menyimpan tanggal (RFC3339, jam diabaikan).
			task.Due = d.UTC().Format(time.RFC3339)
		}
	}
	_ = ctx.React("📝")
	srv, err := tasksService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 25*time.Second)
	defer cancel()
	created, err := srv.Tasks.Insert("@default", task).Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal menambah tugas: " + err.Error())
	}
	return ctx.Reply("✅ Tugas ditambah: *" + created.Title + "*")
}

func tugasDone(ctx *ContextBot, rest string) error {
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || n < 1 {
		return ctx.Reply("⚠️ Format: `tugas selesai <nomor>` (nomor dari `tugas`).")
	}
	_ = ctx.React("✅")
	srv, err := tasksService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 25*time.Second)
	defer cancel()
	res, err := srv.Tasks.List("@default").ShowCompleted(false).MaxResults(50).Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal ambil tugas: " + err.Error())
	}
	if n > len(res.Items) {
		return ctx.Reply(fmt.Sprintf("⚠️ Nomor %d tidak ada (hanya %d tugas).", n, len(res.Items)))
	}
	target := res.Items[n-1]
	target.Status = "completed"
	if _, err := srv.Tasks.Patch("@default", target.Id, target).Context(c).Do(); err != nil {
		return ctx.Reply("❌ Gagal menandai selesai: " + err.Error())
	}
	return ctx.Reply("🎉 Tugas *" + target.Title + "* ditandai selesai.")
}
