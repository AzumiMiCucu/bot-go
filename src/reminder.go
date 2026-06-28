package src

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/tasks/v1"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// REMINDER OTOMATIS (owner) — bot proaktif japri owner berdasarkan Google:
//   1. Pengingat per-acara: ~30 menit sebelum acara Calendar dimulai.
//   2. Briefing pagi (sekali/hari, jam 7+ WIB): agenda + tugas hari ini.
// Aktif hanya bila Google terhubung. Toggle: command `reminder on/off`.
// =================================================================

const (
	reminderBriefHour = 7  // jam (WIB) mulai briefing pagi
	reminderLeadMin   = 30 // ingatkan acara N menit sebelum mulai
)

var (
	reminderEnabled int32 = 1
	reminderClient  *whatsmeow.Client
	reminderLoc     = loadJakartaTZ()
	remindedEvents  = map[string]bool{} // eventID yang sudah diingatkan (di-reset harian)
	lastBriefDate   string              // "2006-01-02" briefing terakhir
)

func loadJakartaTZ() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		return time.FixedZone("WIB", 7*3600)
	}
	return loc
}

// SetReminder mengaktifkan/menonaktifkan reminder saat runtime.
func SetReminder(on bool) {
	if on {
		atomic.StoreInt32(&reminderEnabled, 1)
	} else {
		atomic.StoreInt32(&reminderEnabled, 0)
	}
}

// ReminderOn melaporkan status reminder.
func ReminderOn() bool { return atomic.LoadInt32(&reminderEnabled) == 1 }

// StartReminderScheduler menjalankan pengecekan reminder tiap 5 menit.
func StartReminderScheduler(client *whatsmeow.Client) {
	reminderClient = client
	go func() {
		time.Sleep(25 * time.Second) // beri waktu koneksi mantap
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		runReminderTick()
		for range ticker.C {
			runReminderTick()
		}
	}()
	fmt.Println("[SCHEDULER] ⏰ Reminder Google (acara & tugas) aktif (cek tiap 5 menit; toggle: `reminder on/off`).")
}

func runReminderTick() {
	if !ReminderOn() || reminderClient == nil {
		return
	}
	if AppConfig == nil || AppConfig.OwnerNumber == "" || !GoogleReady() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpc, err := GoogleClient(ctx)
	if err != nil {
		return
	}
	cal, err := calendar.NewService(ctx, option.WithHTTPClient(httpc))
	if err != nil {
		return
	}

	now := time.Now()

	// 1) Pengingat per-acara (≤ reminderLeadMin menit sebelum mulai).
	tMin := now.Format(time.RFC3339)
	tMax := now.Add(time.Duration(reminderLeadMin+5) * time.Minute).Format(time.RFC3339)
	if evs, err := cal.Events.List("primary").ShowDeleted(false).SingleEvents(true).
		TimeMin(tMin).TimeMax(tMax).OrderBy("startTime").Do(); err == nil {
		for _, e := range evs.Items {
			if e.Start == nil || e.Start.DateTime == "" || remindedEvents[e.Id] {
				continue
			}
			st, err := time.Parse(time.RFC3339, e.Start.DateTime)
			if err != nil {
				continue
			}
			mins := time.Until(st).Minutes()
			if mins <= float64(reminderLeadMin) && mins >= -2 {
				remindedEvents[e.Id] = true
				loc := ""
				if e.Location != "" {
					loc = "\n📍 " + e.Location
				}
				sendToOwner(fmt.Sprintf("⏰ *Pengingat acara*\n\n*%s*\n🕒 %s (%d menit lagi)%s",
					e.Summary, st.In(reminderLoc).Format("15:04"), int(mins+0.5), loc))
			}
		}
	}

	// 2) Briefing pagi sekali sehari (jam 7–11 WIB).
	today := now.In(reminderLoc).Format("2006-01-02")
	if h := now.In(reminderLoc).Hour(); lastBriefDate != today && h >= reminderBriefHour && h < 12 {
		lastBriefDate = today
		remindedEvents = map[string]bool{} // reset dedup harian
		sendMorningBrief(ctx, cal, httpc)
	}
}

// TriggerMorningBrief mengirim briefing sekarang juga (untuk `reminder test`).
func TriggerMorningBrief() error {
	if !GoogleReady() {
		return fmt.Errorf("Google belum terhubung — jalankan `gauth`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpc, err := GoogleClient(ctx)
	if err != nil {
		return err
	}
	cal, err := calendar.NewService(ctx, option.WithHTTPClient(httpc))
	if err != nil {
		return err
	}
	sendMorningBrief(ctx, cal, httpc)
	return nil
}

func sendMorningBrief(ctx context.Context, cal *calendar.Service, httpc *http.Client) {
	loc := reminderLoc
	now := time.Now().In(loc)
	startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	endDay := startDay.AddDate(0, 0, 1)

	var sb strings.Builder
	name := "Azmi"
	if AppConfig != nil && AppConfig.OwnerName != "" {
		name = AppConfig.OwnerName
	}
	sb.WriteString("☀️ *Selamat pagi, " + name + "!*\n_Ringkasan hari ini, " + now.Format("Mon, 02 Jan 2006") + "_\n\n")

	// Agenda hari ini.
	sb.WriteString("📅 *Agenda:*\n")
	if evs, err := cal.Events.List("primary").ShowDeleted(false).SingleEvents(true).
		TimeMin(startDay.Format(time.RFC3339)).TimeMax(endDay.Format(time.RFC3339)).
		OrderBy("startTime").Do(); err == nil && len(evs.Items) > 0 {
		for _, e := range evs.Items {
			when := "Seharian "
			if e.Start != nil && e.Start.DateTime != "" {
				if t, err := time.Parse(time.RFC3339, e.Start.DateTime); err == nil {
					when = t.In(loc).Format("15:04") + " "
				}
			}
			sb.WriteString("• " + when + "— " + e.Summary + "\n")
		}
	} else {
		sb.WriteString("_Tidak ada acara hari ini._\n")
	}

	// Tugas tertunda.
	sb.WriteString("\n📝 *Tugas:*\n")
	if tk, err := tasks.NewService(ctx, option.WithHTTPClient(httpc)); err == nil {
		if tl, err := tk.Tasks.List("@default").ShowCompleted(false).MaxResults(50).Do(); err == nil && len(tl.Items) > 0 {
			for i, t := range tl.Items {
				due := ""
				if t.Due != "" {
					if d, err := time.Parse(time.RFC3339, t.Due); err == nil {
						due = " (⏰ " + d.In(loc).Format("02 Jan") + ")"
					}
				}
				sb.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, t.Title, due))
			}
		} else {
			sb.WriteString("_Tidak ada tugas tertunda._\n")
		}
	}

	sb.WriteString("\n_Semangat! Ketik biasa kalau butuh bantuan apa pun._")
	sendToOwner(sb.String())
}

func sendToOwner(text string) {
	if reminderClient == nil || AppConfig == nil || AppConfig.OwnerNumber == "" {
		return
	}
	jid := types.NewJID(AppConfig.OwnerNumber, types.DefaultUserServer)
	_, _ = reminderClient.SendMessage(context.Background(), jid,
		&waProto.Message{Conversation: proto.String(CleanUTF8(text))})
}
