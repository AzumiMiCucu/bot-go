package src

import (
	"database/sql"
	"fmt"
	"time"
)

// =================================================================
// LIVE MESSAGE STORE — database/msg.db (terpisah dari bot.db)
// =================================================================
// Tiap pesan yang tiba LIVE diringkas (id, pengirim, verdict deteksi, sinyal,
// waktu) lalu disimpan ke msg.db. Tujuan: histori + bahan STATISTIK bot nanti.
//
// Hemat & tidak memperlambat respon:
//   - Penulisan ASINKRON via channel buffered → tak menyentuh hot-path handler.
//   - Goroutine writer nge-BATCH insert dalam transaksi (flush per N baris / 2 dtk).
//   - BOUNDED: auto-prune baris > retensi (7 hari) + cap jumlah baris maksimum.
//   - Teks dipotong (msgTextMax) agar DB tidak membengkak.
// =================================================================

const (
	msgRetention  = 7 * 24 * time.Hour // simpan 7 hari
	msgMaxRows    = 200000             // batas keras jumlah baris
	msgFlushEvery = 2 * time.Second    // flush batch berkala
	msgBatchSize  = 100                // flush bila batch penuh
	msgTextMax    = 500                // potong teks pesan
	msgChanBuffer = 1000               // buffer channel; penuh → pesan dibuang (non-blok)
)

type liveMessage struct {
	id, chat, sender, pushName  string
	text, verdict, deviceClass  string
	isGroup, isMedia            bool
	hasDeviceList, hasMsgSecret bool
	isBaileysID, isBot          bool
	baileysScore                int
	ts                          int64
}

var (
	msgDB   *sql.DB
	msgChan chan liveMessage
)

// InitMessageStore membuka database/msg.db, membuat tabel, dan menjalankan
// goroutine writer + pruner. Dipanggil sekali dari main (setelah InitDatabase).
func InitMessageStore() error {
	db, err := sql.Open("sqlite3", "database/msg.db?mode=rwc&cache=shared&_journal=wal")
	if err != nil {
		return fmt.Errorf("buka msg.db: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)
	db.Exec("PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA temp_store = MEMORY;")

	schema := `
	CREATE TABLE IF NOT EXISTS messages (
		id              TEXT,
		chat            TEXT,
		sender          TEXT,
		pushname        TEXT,
		is_group        INTEGER,
		is_media        INTEGER,
		text            TEXT,
		verdict         TEXT,
		device_class    TEXT,
		baileys_score   INTEGER,
		has_device_list INTEGER,
		has_msg_secret  INTEGER,
		is_baileys_id   INTEGER,
		is_bot          INTEGER,
		ts              INTEGER
	);
	CREATE INDEX IF NOT EXISTS idx_msg_ts ON messages(ts);
	CREATE INDEX IF NOT EXISTS idx_msg_chat ON messages(chat);
	CREATE INDEX IF NOT EXISTS idx_msg_verdict ON messages(verdict);

	-- AKUMULATOR PERMANEN (TIDAK ikut di-prune) — sumber statistik sepanjang masa.
	CREATE TABLE IF NOT EXISTS stat_counters (
		k TEXT PRIMARY KEY,
		v INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS stat_senders (
		sender   TEXT PRIMARY KEY,
		pushname TEXT,
		total    INTEGER DEFAULT 0,
		media    INTEGER DEFAULT 0,
		bot      INTEGER DEFAULT 0,
		last_ts  INTEGER
	);
	CREATE INDEX IF NOT EXISTS idx_statsenders_total ON stat_senders(total);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("buat tabel msg.db: %w", err)
	}

	// Migrasi idempotent untuk msg.db lama (dibuat sebelum kolom is_media ada).
	// CREATE TABLE IF NOT EXISTS tak mengubah tabel lama, jadi tambah kolom di sini.
	// Error "duplicate column name" pada DB baru diabaikan dengan sengaja.
	db.Exec("ALTER TABLE messages ADD COLUMN is_media INTEGER DEFAULT 0")

	msgDB = db

	// Backfill akumulator dari data `messages` yang sudah ada (sekali saja, bila
	// akumulator masih kosong) agar statistik lama tidak hilang.
	backfillAccumulators(db)
	msgChan = make(chan liveMessage, msgChanBuffer)
	go msgWriter()
	go msgPruner()
	fmt.Println("[DB] msg.db (live message store) berhasil diinisialisasi")
	return nil
}

// StoreLiveMessage mengantre ringkasan sebuah pesan live untuk ditulis ke msg.db.
// Non-blok: bila buffer penuh, pesan dibuang diam-diam (lebih baik kehilangan
// statistik daripada memperlambat bot).
func StoreLiveMessage(det BotDetectionResult, chat, text string, isGroup, isMedia bool, ts time.Time) {
	if msgChan == nil || det.MessageID == "" {
		return
	}
	if len(text) > msgTextMax {
		text = text[:msgTextMax]
	}
	lm := liveMessage{
		id:            det.MessageID,
		chat:          chat,
		sender:        det.SenderUser,
		pushName:      det.PushName,
		text:          text,
		verdict:       det.Verdict,
		deviceClass:   det.DeviceClass,
		isGroup:       isGroup,
		isMedia:       isMedia,
		hasDeviceList: det.HasDeviceListMD,
		hasMsgSecret:  det.HasMessageSecret,
		isBaileysID:   det.IsBaileysID,
		isBot:         det.Verdict == VerdictBot || det.Verdict == VerdictBaileys,
		baileysScore:  det.BaileysScore,
		ts:            ts.Unix(),
	}
	if lm.ts <= 0 {
		lm.ts = time.Now().Unix()
	}
	select {
	case msgChan <- lm:
	default: // buffer penuh → buang, jangan blokir
	}
}

func msgWriter() {
	ticker := time.NewTicker(msgFlushEvery)
	defer ticker.Stop()

	batch := make([]liveMessage, 0, msgBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		tx, err := msgDB.Begin()
		if err != nil {
			batch = batch[:0]
			return
		}
		ins, err := tx.Prepare(`INSERT INTO messages
			(id, chat, sender, pushname, is_group, is_media, text, verdict, device_class,
			 baileys_score, has_device_list, has_msg_secret, is_baileys_id, is_bot, ts)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			fmt.Printf("[msg.db] prepare insert gagal: %v\n", err)
			tx.Rollback()
			batch = batch[:0]
			return
		}
		// UPSERT akumulator pengirim (permanen, tak ikut prune).
		ups, err := tx.Prepare(`INSERT INTO stat_senders (sender, pushname, total, media, bot, last_ts)
			VALUES (?,?,1,?,?,?)
			ON CONFLICT(sender) DO UPDATE SET
				pushname=excluded.pushname,
				total=total+1, media=media+excluded.media, bot=bot+excluded.bot,
				last_ts=excluded.last_ts`)
		if err != nil {
			ins.Close()
			tx.Rollback()
			batch = batch[:0]
			return
		}
		// Counter global permanen (key → +delta).
		counters := map[string]int{}
		bump := func(k string, n int) { counters[k] += n }

		for _, m := range batch {
			ins.Exec(m.id, m.chat, m.sender, m.pushName, b2i(m.isGroup), b2i(m.isMedia), m.text,
				m.verdict, m.deviceClass, m.baileysScore, b2i(m.hasDeviceList),
				b2i(m.hasMsgSecret), b2i(m.isBaileysID), b2i(m.isBot), m.ts)
			if m.sender != "" {
				ups.Exec(m.sender, m.pushName, b2i(m.isMedia), b2i(m.isBot), m.ts)
			}

			bump("total", 1)
			if m.isMedia {
				bump("media", 1)
			}
			if m.isGroup {
				bump("group", 1)
			} else {
				bump("private", 1)
			}
			bump("verdict_"+nz(m.verdict, "unknown"), 1)
			bump("dev_"+nz(m.deviceClass, "unknown"), 1)
		}
		ins.Close()
		ups.Close()
		flushCounters(tx, counters)
		tx.Commit()
		batch = batch[:0]
	}

	for {
		select {
		case m := <-msgChan:
			batch = append(batch, m)
			if len(batch) >= msgBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// msgPruner menjaga ukuran msg.db tetap terbatas: hapus baris lebih tua dari
// retensi, lalu pangkas bila melebihi batas baris (sisakan yang terbaru).
func msgPruner() {
	prune := func() {
		cutoff := time.Now().Add(-msgRetention).Unix()
		msgDB.Exec("DELETE FROM messages WHERE ts < ?", cutoff)
		msgDB.Exec(`DELETE FROM messages WHERE rowid IN (
			SELECT rowid FROM messages ORDER BY ts DESC LIMIT -1 OFFSET ?)`, msgMaxRows)
	}
	prune() // sekali saat start
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		prune()
	}
}

// backfillAccumulators mengisi stat_counters & stat_senders dari tabel messages
// yang sudah ada — hanya bila akumulator masih kosong (sekali jalan). Berguna saat
// upgrade dari msg.db lama yang sempat menulis ke messages tapi belum punya
// lapisan akumulator.
func backfillAccumulators(db *sql.DB) {
	var existing int
	db.QueryRow("SELECT COUNT(*) FROM stat_counters").Scan(&existing)
	if existing > 0 {
		return // sudah ada akumulasi → jangan timpa
	}
	var rows int
	db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&rows)
	if rows == 0 {
		return // tak ada data lama
	}

	counters := map[string]int{}
	scanGroup := func(query, prefix string) {
		r, err := db.Query(query)
		if err != nil {
			return
		}
		defer r.Close()
		for r.Next() {
			var k string
			var n int
			if r.Scan(&k, &n) == nil {
				counters[prefix+nz(k, "unknown")] = n
			}
		}
	}

	counters["total"] = rows
	var media, grp int
	db.QueryRow("SELECT COALESCE(SUM(is_media),0), COALESCE(SUM(is_group),0) FROM messages").Scan(&media, &grp)
	counters["media"] = media
	counters["group"] = grp
	counters["private"] = rows - grp
	scanGroup("SELECT verdict, COUNT(*) FROM messages GROUP BY verdict", "verdict_")
	scanGroup("SELECT device_class, COUNT(*) FROM messages GROUP BY device_class", "dev_")

	tx, err := db.Begin()
	if err != nil {
		return
	}
	if c, e := tx.Prepare(`INSERT INTO stat_counters (k,v) VALUES (?,?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`); e == nil {
		for k, v := range counters {
			c.Exec(k, v)
		}
		c.Close()
	}
	tx.Exec(`INSERT INTO stat_senders (sender, pushname, total, media, bot, last_ts)
		SELECT sender, MAX(pushname), COUNT(*), COALESCE(SUM(is_media),0), COALESCE(SUM(is_bot),0), MAX(ts)
		FROM messages WHERE sender != '' GROUP BY sender
		ON CONFLICT(sender) DO UPDATE SET
			pushname=excluded.pushname, total=excluded.total,
			media=excluded.media, bot=excluded.bot, last_ts=excluded.last_ts`)
	tx.Commit()
	fmt.Printf("[msg.db] backfill akumulator dari %d pesan lama\n", rows)
}

// flushCounters menerapkan akumulasi counter ke stat_counters dalam transaksi.
func flushCounters(tx *sql.Tx, counters map[string]int) {
	if len(counters) == 0 {
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO stat_counters (k, v) VALUES (?, ?)
		ON CONFLICT(k) DO UPDATE SET v = v + excluded.v`)
	if err != nil {
		return
	}
	defer stmt.Close()
	for k, v := range counters {
		stmt.Exec(k, v)
	}
}

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ====================== STATISTIK AKUMULATIF (PERMANEN) ======================

// AccumCounters mengembalikan semua counter permanen (total, media, group,
// private, verdict_*, dev_*). Tak terpengaruh prune.
func AccumCounters() map[string]int {
	out := make(map[string]int)
	if msgDB == nil {
		return out
	}
	rows, err := msgDB.Query("SELECT k, v FROM stat_counters")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v int
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	return out
}

// SenderStat = ringkasan akumulatif satu pengirim sepanjang masa.
type SenderStat struct {
	Sender   string
	PushName string
	Total    int
	Media    int
	Bot      int
}

func querySenders(where string, limit int) []SenderStat {
	var out []SenderStat
	if msgDB == nil {
		return out
	}
	rows, err := msgDB.Query(`SELECT sender, pushname, total, media, bot
		FROM stat_senders `+where+` ORDER BY total DESC LIMIT ?`, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s SenderStat
		if rows.Scan(&s.Sender, &s.PushName, &s.Total, &s.Media, &s.Bot) == nil {
			out = append(out, s)
		}
	}
	return out
}

// TopSendersAllTime: pengirim paling aktif sepanjang masa (akumulatif).
func TopSendersAllTime(limit int) []SenderStat { return querySenders("", limit) }

// TopBotSendersAllTime: pengirim ber-verdict bot/baileys terbanyak sepanjang masa.
func TopBotSendersAllTime(limit int) []SenderStat { return querySenders("WHERE bot > 0", limit) }

// ====================== STATISTIK 7-HARI (data mentah, ikut prune) ======================

// CountByVerdict mengembalikan jumlah pesan per-verdict sejak waktu tertentu.
func CountByVerdict(since time.Time) map[string]int {
	out := make(map[string]int)
	if msgDB == nil {
		return out
	}
	rows, err := msgDB.Query("SELECT verdict, COUNT(*) FROM messages WHERE ts >= ? GROUP BY verdict", since.Unix())
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		var c int
		if rows.Scan(&v, &c) == nil {
			out[v] = c
		}
	}
	return out
}

// BotSenderStat = ringkasan pengirim ber-verdict bot/baileys.
type BotSenderStat struct {
	Sender   string
	PushName string
	Count    int
}

// TopBotSenders mengembalikan pengirim dengan pesan bot/baileys terbanyak.
func TopBotSenders(limit int) []BotSenderStat {
	var out []BotSenderStat
	if msgDB == nil {
		return out
	}
	rows, err := msgDB.Query(`SELECT sender, MAX(pushname), COUNT(*) c
		FROM messages WHERE is_bot = 1 GROUP BY sender ORDER BY c DESC LIMIT ?`, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s BotSenderStat
		if rows.Scan(&s.Sender, &s.PushName, &s.Count) == nil {
			out = append(out, s)
		}
	}
	return out
}

// ====================== STATISTIK PER-GRUP (LIVE, dari msg.db) ======================
//
// Dipakai gstats agar laporan grup berkolaborasi dengan data live msg.db:
// rasio manusia vs bot, sebaran device, jam tersibuk, pengirim unik — semuanya
// data JUJUR langsung dari pesan yang benar-benar tiba (bukan angka karangan).
// Bertumpu pada tabel `messages` yang ikut prune (retensi 7 hari), jadi nilainya
// mencerminkan aktivitas beberapa hari terakhir.

// GroupLiveStat = rangkuman aktivitas live sebuah grup pada jendela `Days` hari.
type GroupLiveStat struct {
	Days          int
	Total         int            // total pesan tercatat
	Media         int            // pesan media
	HumanMsgs     int            // pesan ber-verdict bukan bot
	BotMsgs       int            // pesan ber-verdict bot/baileys
	UniqueSenders int            // jumlah pengirim unik
	ActiveToday   int            // pesan sejak tengah malam (waktu lokal)
	Hourly        [24]int        // sebaran pesan per jam (akumulasi seluruh jendela)
	PeakHour      int            // jam tersibuk (0-23), -1 bila tak ada data
	PeakHourVal   int            // jumlah pesan pada jam tersibuk
	Devices       map[string]int // device_class → jumlah
	Verdicts      map[string]int // verdict → jumlah
}

// GroupLiveStats menghitung rangkuman live untuk satu grup (chat = JID non-AD,
// format sama dengan yang dipakai handler: evt.Info.Chat.ToNonAD().String()).
func GroupLiveStats(chat string, days int) GroupLiveStat {
	out := GroupLiveStat{
		Days:     days,
		PeakHour: -1,
		Devices:  map[string]int{},
		Verdicts: map[string]int{},
	}
	if msgDB == nil || chat == "" {
		return out
	}
	if days <= 0 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	startToday := time.Now().Truncate(24 * time.Hour).Unix()

	// Agregat utama dalam satu query.
	msgDB.QueryRow(`SELECT
			COUNT(*),
			COALESCE(SUM(is_media),0),
			COALESCE(SUM(is_bot),0),
			COUNT(DISTINCT sender)
		FROM messages WHERE chat = ? AND ts >= ?`,
		chat, since).Scan(&out.Total, &out.Media, &out.BotMsgs, &out.UniqueSenders)
	out.HumanMsgs = out.Total - out.BotMsgs
	if out.HumanMsgs < 0 {
		out.HumanMsgs = 0
	}

	msgDB.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat = ? AND ts >= ?`,
		chat, startToday).Scan(&out.ActiveToday)

	// Sebaran per jam (waktu lokal).
	if rows, err := msgDB.Query(`SELECT CAST(strftime('%H', ts, 'unixepoch', 'localtime') AS INTEGER), COUNT(*)
		FROM messages WHERE chat = ? AND ts >= ? GROUP BY 1`, chat, since); err == nil {
		for rows.Next() {
			var h, c int
			if rows.Scan(&h, &c) == nil && h >= 0 && h < 24 {
				out.Hourly[h] = c
				if c > out.PeakHourVal {
					out.PeakHourVal = c
					out.PeakHour = h
				}
			}
		}
		rows.Close()
	}

	// Sebaran device & verdict.
	scan := func(col string, dst map[string]int) {
		rows, err := msgDB.Query(`SELECT `+col+`, COUNT(*) FROM messages
			WHERE chat = ? AND ts >= ? GROUP BY `+col, chat, since)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var c int
			if rows.Scan(&k, &c) == nil {
				dst[nz(k, "unknown")] += c
			}
		}
	}
	scan("device_class", out.Devices)
	scan("verdict", out.Verdicts)
	return out
}

// GroupTopSendersLive mengembalikan pengirim paling aktif di sebuah grup pada
// jendela `days` hari (berdasarkan data live msg.db, bukan akumulator bot.db).
func GroupTopSendersLive(chat string, days, limit int) []BotSenderStat {
	var out []BotSenderStat
	if msgDB == nil || chat == "" {
		return out
	}
	if days <= 0 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	rows, err := msgDB.Query(`SELECT sender, MAX(pushname), COUNT(*) c
		FROM messages WHERE chat = ? AND ts >= ? AND sender != ''
		GROUP BY sender ORDER BY c DESC LIMIT ?`, chat, since, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s BotSenderStat
		if rows.Scan(&s.Sender, &s.PushName, &s.Count) == nil {
			out = append(out, s)
		}
	}
	return out
}

// TopDevice mengembalikan kelas device dengan pesan terbanyak beserta jumlahnya.
func (g GroupLiveStat) TopDevice() (string, int) {
	best, bestN := "unknown", 0
	for k, v := range g.Devices {
		if v > bestN {
			best, bestN = k, v
		}
	}
	return best, bestN
}

// CloseMessageStore menutup koneksi msg.db (dipanggil saat shutdown).
func CloseMessageStore() error {
	if msgDB == nil {
		return nil
	}
	return msgDB.Close()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
