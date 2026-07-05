// src/reminder.go
//
// Fitur REMINDER / ALARM (owner-only) berbasis PANGGILAN.
//
// Owner menjadwalkan pengingat; saat waktunya tiba bot MENELEPON owner (dering =
// alarm) via subsistem call (meowcaller). Isi pengingat dikirim juga sebagai
// catatan teks ke owner supaya tahu konteksnya (panggilan sendiri tak membawa
// teks). Opsional: sebuah file audio bisa diputar otomatis saat panggilan
// diangkat (mis. rekaman azan / suara pengingat).
//
// Dua jenis:
//   - one-shot : sekali picu lalu dihapus.
//   - harian   : dipicu tiap hari pada jam yang sama (di-arm ulang otomatis).
//
// Persist ke database/reminders.json agar tahan restart. Saat load, pengingat
// one-shot yang sudah lewat dipicu sekali (dengan catatan "terlambat"); pengingat
// harian yang lewat dimajukan ke kemunculan berikutnya.
package src

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
)

// Reminder adalah satu entri pengingat.
type Reminder struct {
	ID      int       `json:"id"`
	FireAt  time.Time `json:"fireAt"`  // waktu picu berikutnya
	Message string    `json:"message"` // isi pengingat (dikirim sebagai catatan teks)
	Audio   string    `json:"audio"`   // opsional: path file audio diputar saat diangkat
	Daily   bool      `json:"daily"`   // true = ulang tiap hari pada jam FireAt
	Created time.Time `json:"created"`
}

const remindersFilePath = "database/reminders.json"

var (
	reminders   []*Reminder
	remindersMu sync.Mutex
	nextRemID   = 1
)

// LoadReminders membaca pengingat dari disk. Dipanggil sekali saat start
// (StartReminderScheduler melakukannya otomatis).
func loadReminders() {
	data, err := os.ReadFile(remindersFilePath)
	if err != nil {
		return // file belum ada = tak ada pengingat
	}
	var stored []*Reminder
	if err := json.Unmarshal(data, &stored); err != nil {
		fmt.Printf("[REMINDER] ⚠️ Gagal parse %s: %v\n", remindersFilePath, err)
		return
	}
	reminders = stored
	for _, r := range reminders {
		if r.ID >= nextRemID {
			nextRemID = r.ID + 1
		}
	}
}

// saveReminders menulis pengingat ke disk. Pemanggil WAJIB memegang remindersMu.
func saveReminders() {
	data, err := json.MarshalIndent(reminders, "", "  ")
	if err != nil {
		fmt.Printf("[REMINDER] ⚠️ Gagal marshal: %v\n", err)
		return
	}
	if err := os.WriteFile(remindersFilePath, data, 0644); err != nil {
		fmt.Printf("[REMINDER] ⚠️ Gagal simpan %s: %v\n", remindersFilePath, err)
	}
}

// AddReminder menambah pengingat baru. Mengembalikan pengingat yang tersimpan.
func AddReminder(fireAt time.Time, message, audio string, daily bool) *Reminder {
	remindersMu.Lock()
	defer remindersMu.Unlock()

	r := &Reminder{
		ID:      nextRemID,
		FireAt:  fireAt,
		Message: message,
		Audio:   audio,
		Daily:   daily,
		Created: time.Now(),
	}
	nextRemID++
	reminders = append(reminders, r)
	saveReminders()
	return r
}

// DelReminder menghapus pengingat berdasarkan ID. Mengembalikan true bila ada.
func DelReminder(id int) bool {
	remindersMu.Lock()
	defer remindersMu.Unlock()

	for i, r := range reminders {
		if r.ID == id {
			reminders = append(reminders[:i], reminders[i+1:]...)
			saveReminders()
			return true
		}
	}
	return false
}

// ClearReminders menghapus semua pengingat. Mengembalikan jumlah yang dihapus.
func ClearReminders() int {
	remindersMu.Lock()
	defer remindersMu.Unlock()

	n := len(reminders)
	reminders = nil
	saveReminders()
	return n
}

// ListReminders mengembalikan salinan daftar pengingat, terurut waktu picu.
func ListReminders() []Reminder {
	remindersMu.Lock()
	defer remindersMu.Unlock()

	out := make([]Reminder, 0, len(reminders))
	for _, r := range reminders {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FireAt.Before(out[j].FireAt) })
	return out
}

// StartReminderScheduler memuat pengingat tersimpan lalu menjalankan loop pemicu.
// Ticker berdenyut tiap 15 detik (presisi cukup untuk alarm menit-an).
func StartReminderScheduler(client *whatsmeow.Client) {
	loadReminders()

	// Tangani pengingat yang sudah lewat saat bot mati.
	catchUpReminders(client)

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			checkReminders(client)
		}
	}()

	remindersMu.Lock()
	n := len(reminders)
	remindersMu.Unlock()
	fmt.Printf("[SCHEDULER] ⏰ Reminder-call owner aktif (%d terjadwal; cek tiap 15 dtk).\n", n)
}

// catchUpReminders memproses pengingat yang jatuh tempo selagi bot mati.
// One-shot yang lewat dipicu sekali (ditandai terlambat); harian dimajukan.
func catchUpReminders(client *whatsmeow.Client) {
	now := time.Now()

	remindersMu.Lock()
	var overdue []*Reminder
	var kept []*Reminder
	for _, r := range reminders {
		if r.FireAt.After(now) {
			kept = append(kept, r)
			continue
		}
		if r.Daily {
			r.FireAt = nextDailyOccurrence(r.FireAt, now)
			kept = append(kept, r)
		} else {
			overdue = append(overdue, r)
		}
	}
	reminders = kept
	saveReminders()
	remindersMu.Unlock()

	for _, r := range overdue {
		late := now.Sub(r.FireAt).Round(time.Minute)
		fireReminderCall(client, r, fmt.Sprintf(" (terlambat %s — bot sempat mati)", HumanizeDur(late)))
	}
}

// checkReminders dipanggil tiap tick; memicu pengingat yang jatuh tempo.
func checkReminders(client *whatsmeow.Client) {
	now := time.Now()

	remindersMu.Lock()
	var due []*Reminder
	var kept []*Reminder
	for _, r := range reminders {
		if now.Before(r.FireAt) {
			kept = append(kept, r)
			continue
		}
		due = append(due, r)
		if r.Daily {
			// Arm ulang untuk besok pada jam yang sama.
			r.FireAt = nextDailyOccurrence(r.FireAt, now)
			kept = append(kept, r)
		}
	}
	if len(due) > 0 {
		reminders = kept
		saveReminders()
	}
	remindersMu.Unlock()

	for _, r := range due {
		fireReminderCall(client, r, "")
	}
}

// fireReminderCall menelepon owner dan mengirim catatan teks isi pengingat.
func fireReminderCall(client *whatsmeow.Client, r *Reminder, suffix string) {
	if AppConfig == nil || AppConfig.OwnerNumber == "" {
		fmt.Println("[REMINDER] ⚠️ OwnerNumber kosong; lewati pemicu.")
		return
	}

	kind := "sekali"
	if r.Daily {
		kind = "harian"
	}

	// Catatan teks lebih dulu agar owner tahu konteks saat panggilan berdering.
	note := fmt.Sprintf("⏰ *PENGINGAT #%d* (%s)%s\n\n%s\n\n📞 _Bot menelepon Anda sebagai alarm..._",
		r.ID, kind, suffix, r.Message)
	notifyOwner(note)

	if CallClient == nil {
		fmt.Println("[REMINDER] ⚠️ Subsistem call belum aktif; hanya kirim teks.")
		return
	}

	// Telepon owner. Bila ada audio, diputar otomatis saat diangkat lalu call
	// ditutup (perilaku bawaan StartCall). Tanpa audio, panggilan cukup berdering
	// sebagai alarm — owner menutup sendiri.
	//
	// Pakai context.Background() (bukan yang ber-timeout): panggilan hidup di
	// goroutine background (OnReady/OnEnd dipanggil belakangan saat diangkat),
	// jadi membatalkan ctx tepat setelah StartCall kembali bisa memutus call.
	callID, _, err := StartCall(context.Background(), AppConfig.OwnerNumber, r.Audio, nil)
	if err != nil {
		fmt.Printf("[REMINDER] ⚠️ Gagal menelepon owner utk pengingat #%d: %v\n", r.ID, err)
		notifyOwner(fmt.Sprintf("⚠️ Gagal menelepon untuk pengingat #%d: %v", r.ID, err))
		return
	}
	fmt.Printf("[SCHEDULER] ⏰ Reminder #%d dipicu → memanggil owner (call %s).\n", r.ID, callID)
}

// nextDailyOccurrence mengembalikan waktu berikutnya (> after) pada jam:menit:detik
// yang sama dengan base.
func nextDailyOccurrence(base, after time.Time) time.Time {
	next := time.Date(after.Year(), after.Month(), after.Day(),
		base.Hour(), base.Minute(), base.Second(), 0, base.Location())
	for !next.After(after) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// HumanizeDur memformat durasi jadi ringkas (mis. "1h5m").
func HumanizeDur(d time.Duration) string {
	if d < time.Minute {
		return "kurang dari 1 menit"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d jam %d menit", h, m)
	case h > 0:
		return fmt.Sprintf("%d jam", h)
	default:
		return fmt.Sprintf("%d menit", m)
	}
}
