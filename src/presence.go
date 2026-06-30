package src

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// SUBSISTEM PRESENCE — status ONLINE / LAST-SEEN member langsung dari WA.
//
// WhatsApp HANYA mengirim events.Presence untuk JID yang sudah kita
// SubscribePresence. Strategi (sesuai permintaan user):
//   - LAZY: saat member mengirim pesan di grup → EnsurePresenceSub (cooldown).
//   - PERIODIK (~7 mnt): subscribe ulang HANYA ke member yang SERING chat
//     (subscription presence WA kedaluwarsa, perlu diperbarui berkala).
// Tidak pernah subscribe SEMUA member (hemat + tak mencurigakan/rate-limit).
//
// State disimpan in-memory + snapshot ke msg.db (presence_state) agar last-seen
// tidak hilang antar-restart.
// =================================================================

const (
	presenceSubCooldown = 5 * time.Minute        // jeda minimum subscribe ulang per-jid
	presenceTickEvery   = 7 * time.Minute        // periode subscribe berkala member aktif
	activeMinCount      = 3                      // min. jumlah pesan agar dianggap "sering chat"
	activeWindow        = 2 * time.Hour          // entri aktivitas lebih tua dari ini dibuang
	activeMaxPerTick    = 60                     // batas atas jumlah subscribe per tick
	presenceSubGap      = 120 * time.Millisecond // jeda antar-subscribe (anti-burst)
)

// presenceEntry = state presence sebuah JID.
type presenceEntry struct {
	Online      bool
	LastSeen    int64 // unix detik; 0 = tak diketahui
	OnlineSince int64 // unix detik saat mulai online (0 bila offline)
	OnlineSecs  int64 // akumulasi total durasi online
	Updated     int64
}

// PresenceInfo = snapshot presence yang diekspor untuk dibaca command.
type PresenceInfo struct {
	Online     bool
	LastSeen   int64
	OnlineSecs int64
	Updated    int64
	Known      bool // true bila JID ini pernah punya data presence
}

var (
	presenceMu     sync.RWMutex
	presenceMap    = map[string]*presenceEntry{} // key = JID.ToNonAD().String()
	presenceByUser = map[string]*presenceEntry{} // key = JID.User (nomor) untuk fallback match

	subMu   sync.Mutex
	subLast = map[string]int64{} // key jid → unix detik subscribe terakhir

	activeMu  sync.Mutex
	activeSet = map[string]*activeEntry{} // key = JID.ToNonAD().String()
)

type activeEntry struct {
	jid   types.JID
	count int
	last  int64
}

// =================================================================
// PENCATATAN PRESENCE (dipanggil dari event handler main.go)
// =================================================================

// RecordPresence memperbarui state dari sebuah events.Presence.
func RecordPresence(from types.JID, unavailable bool, lastSeen time.Time) {
	key := from.ToNonAD().String()
	if key == "" {
		return
	}
	now := time.Now().Unix()

	presenceMu.Lock()
	e := presenceMap[key]
	if e == nil {
		e = &presenceEntry{}
		presenceMap[key] = e
		presenceByUser[from.ToNonAD().User] = e
	}
	if unavailable {
		// Transisi → offline: akumulasi durasi sesi online yang berakhir.
		if e.Online && e.OnlineSince > 0 {
			e.OnlineSecs += now - e.OnlineSince
		}
		e.Online = false
		e.OnlineSince = 0
		if !lastSeen.IsZero() {
			e.LastSeen = lastSeen.Unix()
		} else {
			e.LastSeen = now
		}
	} else {
		// → online. Catat awal sesi bila baru transisi.
		if !e.Online {
			e.OnlineSince = now
		}
		e.Online = true
		e.LastSeen = now
	}
	e.Updated = now
	snap := *e
	presenceMu.Unlock()

	persistPresence(key, snap)
}

// persistPresence menyimpan snapshot ke msg.db (async, non-blok hot-path).
func persistPresence(jid string, e presenceEntry) {
	if msgDB == nil {
		return
	}
	online := 0
	if e.Online {
		online = 1
	}
	go msgDB.Exec(`
		INSERT INTO presence_state (jid, online, last_seen, online_secs, updated)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			online = excluded.online, last_seen = excluded.last_seen,
			online_secs = excluded.online_secs, updated = excluded.updated
	`, jid, online, e.LastSeen, e.OnlineSecs, e.Updated)
}

// loadPresenceState memuat snapshot terakhir dari DB ke memori (dipanggil dari
// InitMessageStore). Semua dimuat sebagai OFFLINE — status online live diisi
// ulang oleh events.Presence setelah bot terkoneksi & subscribe.
func loadPresenceState(db *sql.DB) {
	rows, err := db.Query(`SELECT jid, last_seen, online_secs, updated FROM presence_state`)
	if err != nil {
		return
	}
	defer rows.Close()

	presenceMu.Lock()
	defer presenceMu.Unlock()
	for rows.Next() {
		var jid string
		var e presenceEntry
		if rows.Scan(&jid, &e.LastSeen, &e.OnlineSecs, &e.Updated) != nil {
			continue
		}
		e.Online = false
		e.OnlineSince = 0
		ent := e
		presenceMap[jid] = &ent
		if pj, err := types.ParseJID(jid); err == nil {
			presenceByUser[pj.User] = &ent
		}
	}
}

// =================================================================
// QUERY (dipakai command gstats)
// =================================================================

// PresenceFor mengembalikan info presence untuk sebuah JID (cocokkan via string
// penuh lalu fallback via nomor/User).
func PresenceFor(jid types.JID) PresenceInfo {
	key := jid.ToNonAD().String()
	presenceMu.RLock()
	defer presenceMu.RUnlock()
	e := presenceMap[key]
	if e == nil {
		e = presenceByUser[jid.ToNonAD().User]
	}
	if e == nil {
		return PresenceInfo{}
	}
	return PresenceInfo{
		Online: e.Online, LastSeen: e.LastSeen, OnlineSecs: e.OnlineSecs,
		Updated: e.Updated, Known: true,
	}
}

// =================================================================
// AKTIVITAS & SUBSCRIBE
// =================================================================

// NotePresenceActivity mencatat bahwa `sender` baru saja chat di `group`.
// Dipakai memilih siapa member yang "sering chat" untuk subscribe berkala.
func NotePresenceActivity(group, sender types.JID) {
	key := sender.ToNonAD().String()
	if key == "" {
		return
	}
	now := time.Now().Unix()
	activeMu.Lock()
	e := activeSet[key]
	if e == nil {
		e = &activeEntry{jid: sender.ToNonAD()}
		activeSet[key] = e
	}
	e.count++
	e.last = now
	activeMu.Unlock()
}

// activeTargets mengembalikan member yang sering chat (count ≥ ambang, dalam
// window) untuk disubscribe. Sekalian membersihkan entri kedaluwarsa.
func activeTargets() []types.JID {
	cutoff := time.Now().Add(-activeWindow).Unix()
	activeMu.Lock()
	defer activeMu.Unlock()

	var targets []*activeEntry
	for k, e := range activeSet {
		if e.last < cutoff {
			delete(activeSet, k)
			continue
		}
		if e.count >= activeMinCount {
			targets = append(targets, e)
		}
	}
	// Prioritaskan yang paling sering chat.
	sort.Slice(targets, func(i, j int) bool { return targets[i].count > targets[j].count })

	out := make([]types.JID, 0, len(targets))
	for i, e := range targets {
		if i >= activeMaxPerTick {
			break
		}
		out = append(out, e.jid)
	}
	return out
}

// EnsurePresenceSub men-subscribe presence sebuah JID dengan cooldown agar tak
// spam. Aman dipanggil dari hot-path (network call dilempar ke goroutine).
func EnsurePresenceSub(client *whatsmeow.Client, jid types.JID) {
	if client == nil {
		return
	}
	target := jid.ToNonAD()
	key := target.String()
	now := time.Now().Unix()

	subMu.Lock()
	if now-subLast[key] < int64(presenceSubCooldown.Seconds()) {
		subMu.Unlock()
		return
	}
	subLast[key] = now
	subMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = client.SubscribePresence(ctx, target)
	}()
}

// RequestGroupPresence men-subscribe presence sekumpulan JID secara langsung
// (throttled). Dipakai `gstats online` agar snapshot terisi sebelum dibaca.
// Memberi tahu WA bahwa bot online lebih dulu (syarat menerima presence).
func RequestGroupPresence(client *whatsmeow.Client, jids []types.JID) {
	if client == nil {
		return
	}
	ctx := context.Background()
	_ = client.SendPresence(ctx, types.PresenceAvailable)
	for _, j := range jids {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = client.SubscribePresence(c, j.ToNonAD())
		cancel()
		time.Sleep(presenceSubGap)
	}
}

// StartPresenceScheduler menjalankan subscribe berkala ke member yang sering chat.
func StartPresenceScheduler(client *whatsmeow.Client) {
	if client == nil {
		return
	}
	go func() {
		// Jeda awal agar tidak burst tepat saat startup.
		time.Sleep(45 * time.Second)
		ticker := time.NewTicker(presenceTickEvery)
		defer ticker.Stop()

		run := func() {
			targets := activeTargets()
			if len(targets) == 0 {
				return
			}
			ctx := context.Background()
			_ = client.SendPresence(ctx, types.PresenceAvailable)
			for _, j := range targets {
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				_ = client.SubscribePresence(c, j)
				cancel()

				// Tandai cooldown juga agar lazy-sub tak menduplikasi.
				subMu.Lock()
				subLast[j.String()] = time.Now().Unix()
				subMu.Unlock()

				time.Sleep(presenceSubGap)
			}
		}

		run() // jalankan sekali di awal
		for range ticker.C {
			run()
		}
	}()
}
