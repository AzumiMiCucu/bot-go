package src

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// AUTO-READ: tandai pesan grup sebagai dibaca secara batch tiap 30 menit
// agar notifikasi tidak menumpuk saat WhatsApp dibuka.
// =================================================================

type pendingRead struct {
	Chat      types.JID
	Sender    types.JID
	IDs       []types.MessageID
	Timestamp time.Time
}

var (
	pendingReads   = make(map[string]*pendingRead) // key = chat|sender
	pendingReadsMu sync.Mutex
)

// AddPendingRead menampung pesan grup yang masuk untuk ditandai-dibaca nanti.
// Dipanggil dari handler tiap kali ada pesan grup.
func AddPendingRead(chat, sender types.JID, id types.MessageID, ts time.Time) {
	key := chat.String() + "|" + sender.String()

	pendingReadsMu.Lock()
	defer pendingReadsMu.Unlock()

	pr, ok := pendingReads[key]
	if !ok {
		pr = &pendingRead{Chat: chat, Sender: sender}
		pendingReads[key] = pr
	}
	pr.IDs = append(pr.IDs, id)
	if ts.After(pr.Timestamp) {
		pr.Timestamp = ts
	}
	// Batasi memori: simpan maksimal 100 ID terakhir per pengirim
	if len(pr.IDs) > 100 {
		pr.IDs = pr.IDs[len(pr.IDs)-100:]
	}
}

// StartAutoReadScheduler menjalankan flush auto-read tiap 30 menit.
func StartAutoReadScheduler(client *whatsmeow.Client) {
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			flushPendingReads(client)
		}
	}()
	fmt.Println("[SCHEDULER] ✅ Auto-read grup aktif (tiap 30 menit).")
}

func flushPendingReads(client *whatsmeow.Client) {
	pendingReadsMu.Lock()
	batch := pendingReads
	pendingReads = make(map[string]*pendingRead)
	pendingReadsMu.Unlock()

	if len(batch) == 0 {
		return
	}

	count := 0
	for _, pr := range batch {
		if len(pr.IDs) == 0 {
			continue
		}
		err := client.MarkRead(context.Background(), pr.IDs, pr.Timestamp, pr.Chat, pr.Sender)
		if err == nil {
			count += len(pr.IDs)
		}
	}
	if count > 0 {
		fmt.Printf("[SCHEDULER] 📖 Auto-read: %d pesan ditandai dibaca.\n", count)
	}
}

// =================================================================
// AUTO-CLEAR CHAT: tiap N menit, "bersihkan" chat aktif dengan mengirim app-state
// DeleteChat — menghapus chat dari TAMPILAN akun bot (tersinkron ke perangkat
// tertaut bot). TIDAK menghapus pesan untuk lawan bicara. Chat akan muncul lagi
// otomatis saat ada pesan baru, lalu dibersihkan lagi di siklus berikutnya.
//
// Owner DIKECUALIKAN (riwayat owner tak ikut dihapus). Bisa di-toggle owner via
// command `autoclear on/off` (atomic, tanpa restart).
// =================================================================

type chatRec struct {
	chat   types.JID
	lastTS time.Time
	key    *waCommon.MessageKey
}

var (
	chatTracker   = make(map[string]*chatRec) // key = chat JID string
	chatTrackerMu sync.Mutex

	autoClearEnabled int32 // 0/1 — di-set dari config saat start, bisa di-toggle runtime
)

// RecordChat mencatat chat (+ kunci pesan terakhir) untuk kandidat auto-clear.
// Dipanggil dari handler tiap pesan masuk. fromMe biasanya false (handler abaikan
// pesan sendiri), tapi tetap diparam agar kunci akurat bila dipakai di tempat lain.
func RecordChat(chat, sender types.JID, id types.MessageID, fromMe bool, ts time.Time) {
	if chat.IsEmpty() || id == "" {
		return
	}
	chatN := chat.ToNonAD()
	k := chatN.String()

	chatTrackerMu.Lock()
	defer chatTrackerMu.Unlock()
	rec := chatTracker[k]
	if rec == nil {
		rec = &chatRec{chat: chatN}
		chatTracker[k] = rec
	}
	if !ts.IsZero() {
		rec.lastTS = ts
	} else {
		rec.lastTS = time.Now()
	}
	mk := &waCommon.MessageKey{
		RemoteJID: proto.String(k),
		FromMe:    proto.Bool(fromMe),
		ID:        proto.String(string(id)),
	}
	// Participant hanya relevan di grup (pengirim asli).
	if chatN.Server == types.GroupServer && !sender.IsEmpty() {
		mk.Participant = proto.String(sender.ToNonAD().String())
	}
	rec.key = mk
}

// SetAutoClear mengaktifkan/menonaktifkan auto-clear saat runtime + menyimpan ke
// config. Scheduler tetap berjalan; flag ini yang menentukan apakah siklus bekerja.
func SetAutoClear(on bool) {
	if on {
		atomic.StoreInt32(&autoClearEnabled, 1)
	} else {
		atomic.StoreInt32(&autoClearEnabled, 0)
	}
	if AppConfig != nil {
		AppConfig.AutoClearChat = on
		_ = SaveConfig()
	}
}

// AutoClearOn melaporkan status auto-clear saat ini.
func AutoClearOn() bool { return atomic.LoadInt32(&autoClearEnabled) == 1 }

func autoClearInterval() time.Duration {
	m := 30
	if AppConfig != nil && AppConfig.AutoClearMinutes > 0 {
		m = AppConfig.AutoClearMinutes
	}
	return time.Duration(m) * time.Minute
}

// StartAutoClearScheduler menjalankan siklus auto-clear. Ticker selalu berjalan;
// tiap tick hanya bekerja bila auto-clear aktif (toggle runtime).
func StartAutoClearScheduler(client *whatsmeow.Client) {
	if AppConfig != nil && AppConfig.AutoClearChat {
		atomic.StoreInt32(&autoClearEnabled, 1)
	}
	interval := autoClearInterval()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if AutoClearOn() {
				flushAutoClear(client)
			}
		}
	}()
	state := "NONAKTIF"
	if AutoClearOn() {
		state = "aktif"
	}
	fmt.Printf("[SCHEDULER] 🧹 Auto-clear chat %s (tiap %s; toggle: `autoclear on/off`).\n", state, interval)
}

func flushAutoClear(client *whatsmeow.Client) {
	chatTrackerMu.Lock()
	batch := chatTracker
	chatTracker = make(map[string]*chatRec)
	chatTrackerMu.Unlock()

	if len(batch) == 0 {
		return
	}

	owner := ""
	if AppConfig != nil {
		owner = AppConfig.OwnerNumber
	}

	cleared := 0
	for _, rec := range batch {
		if rec.key == nil {
			continue
		}
		// Jangan hapus chat owner (pertahankan riwayat owner).
		if owner != "" && rec.chat.User == owner {
			continue
		}
		patch := appstate.BuildDeleteChat(rec.chat, rec.lastTS, rec.key, false)
		if err := client.SendAppState(context.Background(), patch); err != nil {
			fmt.Printf("[SCHEDULER] ⚠️ Auto-clear gagal utk %s: %v\n", rec.chat, err)
			continue
		}
		cleared++
		// Jangan banjiri server: jeda kecil antar-patch.
		time.Sleep(300 * time.Millisecond)
	}
	if cleared > 0 {
		fmt.Printf("[SCHEDULER] 🧹 Auto-clear: %d chat dibersihkan dari tampilan bot.\n", cleared)
	}
}
