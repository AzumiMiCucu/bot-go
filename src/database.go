package src

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type UserData struct {
	ID           string    `db:"id" json:"id"`
	Name         string    `db:"name" json:"name"`
	RegisteredAt time.Time `db:"registeredAt" json:"registeredAt"`
	MessageCount int       `db:"messageCount" json:"messageCount"`
	Balance      float64   `db:"balance" json:"balance"`
	LastSeen     time.Time `db:"lastSeen" json:"lastSeen"`
	IsBlocked    bool      `db:"isBlocked" json:"isBlocked"`
	Tier         string    `db:"tier" json:"tier"`
}

type Database struct {
	mu          sync.RWMutex
	db          *sql.DB
	cache       map[string]*UserData
	lastCleanup time.Time
}

type HourlyStat struct {
	Hour    int
	Message int
}

type GroupUserStat struct {
	UserID       string
	Name         string
	MessageCount int
	MediaCount   int
	WordCount    int
	LastActive   time.Time
}
type DailyStat struct {
	Day     int
	Message int
}

var DB *Database

func InitDatabase() {
	sqlDb, err := sql.Open("sqlite3", "database/bot.db?mode=rwc&cache=shared&_journal=wal")
	if err != nil {
		panic(fmt.Sprintf("[DB ERROR] Gagal membuka database: %v", err))
	}

	sqlDb.SetMaxOpenConns(25)
	sqlDb.SetMaxIdleConns(25)
	sqlDb.SetConnMaxLifetime(time.Hour)

	// Optimasi SQLite untuk kecepatan maksimal
	sqlDb.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA temp_store = MEMORY;")

	if err := sqlDb.Ping(); err != nil {
		panic("[DB ERROR] Koneksi database gagal: " + err.Error())
	}

	DB = &Database{
		db:          sqlDb,
		cache:       make(map[string]*UserData),
		lastCleanup: time.Now(),
	}

	DB.createTables()
	go DB.autoCleanupCache() // Start garbage collector untuk cache
	fmt.Println("[DB] Database berhasil diinisialisasi")
}

func (db *Database) createTables() {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		registeredAt DATETIME DEFAULT CURRENT_TIMESTAMP,
		messageCount INTEGER DEFAULT 0,
		balance REAL DEFAULT 2.0,
		lastSeen DATETIME DEFAULT CURRENT_TIMESTAMP,
		isBlocked INTEGER DEFAULT 0,
		tier TEXT DEFAULT 'free'
	);

	CREATE TABLE IF NOT EXISTS command_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		commandName TEXT NOT NULL,
		userId TEXT NOT NULL,
		executedAt DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS command_summary (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		commandName TEXT NOT NULL,
		totalCount INTEGER DEFAULT 1,
		dailyCount INTEGER DEFAULT 1,
		hourlyCount INTEGER DEFAULT 1,
		lastExecuted DATETIME DEFAULT CURRENT_TIMESTAMP,
		createdDate DATE DEFAULT (date('now')),
		createdHour INTEGER DEFAULT 0,
		UNIQUE(commandName, createdDate, createdHour)
	);

	CREATE TABLE IF NOT EXISTS transactions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		userId TEXT NOT NULL,
		commandName TEXT NOT NULL,
		amount REAL NOT NULL,
		type TEXT,
		executedAt DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS referrals (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		code TEXT NOT NULL,
		groupID TEXT NOT NULL,
		claimedBy TEXT NOT NULL,
		reward REAL NOT NULL,
		claimedAt DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(code, groupID)
	);

	CREATE TABLE IF NOT EXISTS group_settings (
		groupID TEXT PRIMARY KEY,
		antibot INTEGER DEFAULT 0,
		updatedAt DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- KV sederhana untuk state lintas-restart (mis. dedup reminder).
	CREATE TABLE IF NOT EXISTS bot_meta (
		k TEXT PRIMARY KEY,
		v TEXT DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS group_trust (
		groupID TEXT NOT NULL,
		userID TEXT NOT NULL,
		addedAt DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (groupID, userID)
	);

	CREATE TABLE IF NOT EXISTS group_linkset (
		groupID TEXT NOT NULL,
		pattern TEXT NOT NULL,
		PRIMARY KEY (groupID, pattern)
	);


	CREATE TABLE IF NOT EXISTS group_stats (
		groupID TEXT NOT NULL,
		userId TEXT NOT NULL,
		messageCount INTEGER DEFAULT 0,
		mediaCount INTEGER DEFAULT 0,
		wordCount INTEGER DEFAULT 0,
		lastActive DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (groupID, userId)
	);
	CREATE TABLE IF NOT EXISTS group_stats_daily (
		groupID TEXT NOT NULL,
		userId TEXT NOT NULL,
		statDate DATE NOT NULL,
		statHour INTEGER NOT NULL,
		messageCount INTEGER DEFAULT 0,
		mediaCount INTEGER DEFAULT 0,
		wordCount INTEGER DEFAULT 0,
		PRIMARY KEY (groupID, userId, statDate, statHour)
	);
	CREATE INDEX IF NOT EXISTS idx_groupstatsdaily_date ON group_stats_daily(statDate);
	CREATE INDEX IF NOT EXISTS idx_groupstatsdaily_group ON group_stats_daily(groupID);
	
	CREATE INDEX IF NOT EXISTS idx_groupstats_group ON group_stats(groupID);
	CREATE INDEX IF NOT EXISTS idx_groupstats_lastActive ON group_stats(lastActive);

	CREATE INDEX IF NOT EXISTS idx_users_id ON users(id);
	CREATE INDEX IF NOT EXISTS idx_users_lastSeen ON users(lastSeen);
	CREATE INDEX IF NOT EXISTS idx_cmdstats_executedAt ON command_stats(executedAt);
	CREATE INDEX IF NOT EXISTS idx_cmdstats_name ON command_stats(commandName);
	CREATE INDEX IF NOT EXISTS idx_cmdsummary_date ON command_summary(createdDate);
	CREATE INDEX IF NOT EXISTS idx_transactions_userId ON transactions(userId);
	CREATE INDEX IF NOT EXISTS idx_referrals_code ON referrals(code);
	`
	if _, err := db.db.Exec(schema); err != nil {
		panic("[DB ERROR] Gagal membuat tabel: " + err.Error())
	}

	// Migrasi idempotent: tambah kolom baru bila belum ada (abaikan error "duplicate column")
	db.db.Exec("ALTER TABLE users ADD COLUMN lastDaily DATETIME")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN antilink TEXT DEFAULT ''")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN selfmode INTEGER DEFAULT 0")
	// grpmode: scope self/public per-grup tri-state (''/self/public). Default grup = SELF.
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN grpmode TEXT DEFAULT ''")

	// Welcome / Goodbye (sambutan & perpisahan grup)
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN welcome INTEGER DEFAULT 0")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN welcomeText TEXT DEFAULT ''")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN welcomeType TEXT DEFAULT 'text'")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN welcomeMedia TEXT DEFAULT ''")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN goodbye INTEGER DEFAULT 0")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN goodbyeText TEXT DEFAULT ''")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN goodbyeType TEXT DEFAULT 'text'")
	db.db.Exec("ALTER TABLE group_settings ADD COLUMN goodbyeMedia TEXT DEFAULT ''")
}

// ===================== KV META (state lintas-restart) =====================

// GetMeta mengembalikan nilai meta untuk key (string kosong bila tak ada).
func (db *Database) GetMeta(key string) string {
	if db == nil || db.db == nil {
		return ""
	}
	var v string
	_ = db.db.QueryRow("SELECT v FROM bot_meta WHERE k = ?", key).Scan(&v)
	return v
}

// SetMeta menyimpan (upsert) nilai meta untuk key.
func (db *Database) SetMeta(key, val string) {
	if db == nil || db.db == nil {
		return
	}
	db.db.Exec(`INSERT INTO bot_meta (k, v) VALUES (?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, val)
}

// HasMeta melaporkan apakah sebuah key ada (dipakai untuk dedup boolean).
func (db *Database) HasMeta(key string) bool {
	if db == nil || db.db == nil {
		return false
	}
	var one int
	err := db.db.QueryRow("SELECT 1 FROM bot_meta WHERE k = ?", key).Scan(&one)
	return err == nil
}

// DeleteMetaPrefix menghapus semua key dengan prefix tertentu (mis. reset harian).
func (db *Database) DeleteMetaPrefix(prefix string) {
	if db == nil || db.db == nil {
		return
	}
	db.db.Exec("DELETE FROM bot_meta WHERE k LIKE ?", prefix+"%")
}

// autoCleanupCache menghapus data user dari map memori jika tidak aktif > 1 jam
func (db *Database) autoCleanupCache() {
	ticker := time.NewTicker(30 * time.Minute)
	for range ticker.C {
		db.mu.Lock()
		now := time.Now()
		cleaned := 0
		for k, v := range db.cache {
			if now.Sub(v.LastSeen) > time.Hour {
				delete(db.cache, k)
				cleaned++
			}
		}
		db.mu.Unlock()
		if cleaned > 0 {
			fmt.Printf("[DB] Cache Cleanup: Dihapus %d user tidak aktif dari memori.\n", cleaned)
		}
	}
}

// =============================================
// USER OPERATIONS
// =============================================

func (db *Database) AddOrUpdateUser(jid, name string) *UserData {
	// 1. Fast path: cache hit pakai RLock supaya jalur panas (tiap pesan) TIDAK
	// menserialkan seluruh goroutine handler di satu write-lock global.
	// messageCount & lastSeen dijadikan DB-authoritative (di-update async);
	// nilai di memori menyusul saat cache di-reload (eviction 1 jam).
	db.mu.RLock()
	if cached, exists := db.cache[jid]; exists {
		snap := *cached // salinan untuk caller → aman dari mutasi konkuren (balance/block)
		db.mu.RUnlock()
		// Update DB asinkron untuk respons bot instan.
		go db.db.Exec("UPDATE users SET name=?, messageCount=messageCount+1, lastSeen=CURRENT_TIMESTAMP WHERE id=?", name, jid)
		return &snap
	}
	db.mu.RUnlock()

	// 2. Slow path: query DB TANPA memegang lock (mengurangi contention)
	userData := &UserData{}
	err := db.db.QueryRow("SELECT id, name, registeredAt, messageCount, balance, lastSeen, isBlocked, tier FROM users WHERE id = ?", jid).
		Scan(&userData.ID, &userData.Name, &userData.RegisteredAt, &userData.MessageCount, &userData.Balance, &userData.LastSeen, &userData.IsBlocked, &userData.Tier)

	if err == sql.ErrNoRows {
		userData = &UserData{
			ID: jid, Name: name, RegisteredAt: time.Now(), MessageCount: 1, Balance: 2.0, LastSeen: time.Now(), IsBlocked: false, Tier: "free",
		}
		db.db.Exec("INSERT INTO users (id,name,registeredAt,messageCount,balance,lastSeen,isBlocked,tier) VALUES (?,?,?,?,?,?,?,?)",
			userData.ID, userData.Name, userData.RegisteredAt, userData.MessageCount, userData.Balance, userData.LastSeen, userData.IsBlocked, userData.Tier)
	} else if err == nil {
		userData.MessageCount++
		userData.Name = name
		userData.LastSeen = time.Now()
		go db.db.Exec("UPDATE users SET name=?, messageCount=messageCount+1, lastSeen=CURRENT_TIMESTAMP WHERE id=?", name, jid)
	} else {
		// Error query lain: kembalikan struct minimal agar tidak nil
		userData = &UserData{ID: jid, Name: name, RegisteredAt: time.Now(), MessageCount: 1, Balance: 2.0, LastSeen: time.Now(), Tier: "free"}
	}

	// 3. Masukkan ke cache (double-check agar tidak menimpa entri yang dibuat goroutine lain)
	db.mu.Lock()
	if existing, ok := db.cache[jid]; ok {
		db.mu.Unlock()
		return existing
	}
	db.cache[jid] = userData
	db.mu.Unlock()
	return userData
}

func (db *Database) DeductBalance(jid string, amount float64) (bool, float64) {
	db.mu.Lock()
	defer db.mu.Unlock()

	user := db.cache[jid]
	if user == nil || user.Balance < amount || user.IsBlocked {
		bal := 0.0
		if user != nil {
			bal = user.Balance
		}
		return false, bal
	}

	user.Balance -= amount
	go func(b float64) {
		db.db.Exec("UPDATE users SET balance=? WHERE id=?", b, jid)
		db.db.Exec("INSERT INTO transactions (userId,commandName,amount,type) VALUES (?,?,?,?)", jid, "command", amount, "deduct")
	}(user.Balance)
	return true, user.Balance
}

func (db *Database) AddBalance(jid string, amount float64) float64 {
	db.mu.Lock()
	defer db.mu.Unlock()

	user := db.cache[jid]
	if user == nil {
		return 0
	}
	user.Balance += amount
	go func(b float64) {
		db.db.Exec("UPDATE users SET balance=? WHERE id=?", b, jid)
		db.db.Exec("INSERT INTO transactions (userId,commandName,amount,type) VALUES (?,?,?,?)", jid, "admin", amount, "add")
	}(user.Balance)
	return user.Balance
}

func (db *Database) BlockUser(jid string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.db.Exec("UPDATE users SET isBlocked=1 WHERE id=?", jid)
	if u, ok := db.cache[jid]; ok {
		u.IsBlocked = true
	}
	return err
}

func (db *Database) UnblockUser(jid string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.db.Exec("UPDATE users SET isBlocked=0 WHERE id=?", jid)
	if u, ok := db.cache[jid]; ok {
		u.IsBlocked = false
	}
	return err
}

// GetUser mengambil data user (read-only, tanpa menambah messageCount).
func (db *Database) GetUser(jid string) *UserData {
	db.mu.Lock()
	defer db.mu.Unlock()
	u := db.getOrLoadUser(jid)
	if u == nil {
		return nil
	}
	// kembalikan salinan agar pemanggil tidak memutasi cache
	cp := *u
	return &cp
}

// getOrLoadUser mengambil user dari cache; jika tidak ada, dimuat dari DB.
// Mengembalikan nil jika user tidak ditemukan di DB. (caller wajib pegang db.mu)
func (db *Database) getOrLoadUser(jid string) *UserData {
	if u, ok := db.cache[jid]; ok {
		return u
	}
	u := &UserData{}
	err := db.db.QueryRow("SELECT id, name, registeredAt, messageCount, balance, lastSeen, isBlocked, tier FROM users WHERE id = ?", jid).
		Scan(&u.ID, &u.Name, &u.RegisteredAt, &u.MessageCount, &u.Balance, &u.LastSeen, &u.IsBlocked, &u.Tier)
	if err != nil {
		return nil
	}
	db.cache[jid] = u
	return u
}

// TransferBalance memindahkan saldo dari satu user ke user lain secara atomik (DB + cache).
func (db *Database) TransferBalance(fromJID, toJID string, amount float64) (bool, error) {
	if amount <= 0 {
		return false, fmt.Errorf("jumlah harus lebih dari 0")
	}
	if fromJID == toJID {
		return false, fmt.Errorf("tidak bisa transfer ke diri sendiri")
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	from := db.getOrLoadUser(fromJID)
	if from == nil {
		return false, fmt.Errorf("pengirim tidak terdaftar")
	}
	if from.IsBlocked {
		return false, fmt.Errorf("akun diblokir")
	}
	if from.Balance < amount {
		return false, fmt.Errorf("saldo tidak mencukupi")
	}
	to := db.getOrLoadUser(toJID)
	if to == nil {
		return false, fmt.Errorf("penerima belum pernah berinteraksi dengan bot")
	}

	// Update DB dalam satu transaksi
	tx, err := db.db.Begin()
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec("UPDATE users SET balance=balance-? WHERE id=?", amount, fromJID); err != nil {
		tx.Rollback()
		return false, err
	}
	if _, err := tx.Exec("UPDATE users SET balance=balance+? WHERE id=?", amount, toJID); err != nil {
		tx.Rollback()
		return false, err
	}
	tx.Exec("INSERT INTO transactions (userId,commandName,amount,type) VALUES (?,?,?,?)", fromJID, "transfer", amount, "deduct")
	tx.Exec("INSERT INTO transactions (userId,commandName,amount,type) VALUES (?,?,?,?)", toJID, "transfer", amount, "add")
	if err := tx.Commit(); err != nil {
		return false, err
	}

	// Update cache setelah DB sukses
	from.Balance -= amount
	to.Balance += amount
	return true, nil
}

// GetTopBalance mengambil daftar user dengan saldo tertinggi (untuk leaderboard).
func (db *Database) GetTopBalance(limit int) []UserData {
	rows, err := db.db.Query("SELECT id, name, balance, messageCount, tier FROM users WHERE isBlocked=0 ORDER BY balance DESC LIMIT ?", limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []UserData
	for rows.Next() {
		var u UserData
		if rows.Scan(&u.ID, &u.Name, &u.Balance, &u.MessageCount, &u.Tier) == nil {
			result = append(result, u)
		}
	}
	return result
}

// GetBalanceRank mengembalikan peringkat user berdasarkan saldo (1 = terkaya).
func (db *Database) GetBalanceRank(jid string) int {
	var rank int
	err := db.db.QueryRow(
		"SELECT COUNT(*)+1 FROM users WHERE isBlocked=0 AND balance > (SELECT balance FROM users WHERE id=?)", jid,
	).Scan(&rank)
	if err != nil {
		return 0
	}
	return rank
}

// ClaimReferral mencatat klaim referral. UNIQUE(code, groupID) menjamin hanya
// satu pemenang per grup. Mengembalikan (menang, saldoBaru).
func (db *Database) ClaimReferral(code, groupID, userJID string, reward float64) (bool, float64) {
	res, err := db.db.Exec(
		"INSERT OR IGNORE INTO referrals (code, groupID, claimedBy, reward) VALUES (?,?,?,?)",
		code, groupID, userJID, reward,
	)
	if err != nil {
		return false, 0
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Sudah ada pemenang di grup ini untuk kode ini
		return false, 0
	}

	// Pastikan user ada di cache lalu tambahkan reward
	db.mu.Lock()
	db.getOrLoadUser(userJID)
	db.mu.Unlock()
	newBal := db.AddBalance(userJID, reward)
	return true, newBal
}

// =============================================
// STATS OPERATIONS (DIBUTUHKAN DASHBOARD)
// =============================================

func (db *Database) AddCommandStat(commandName, userID string) error {
	_, err := db.db.Exec("INSERT INTO command_stats (commandName, userId) VALUES (?, ?)", commandName, userID)
	if err != nil {
		return err
	}

	now := time.Now()
	_, err = db.db.Exec(`
		INSERT INTO command_summary (commandName, totalCount, dailyCount, hourlyCount, createdDate, createdHour, lastExecuted)
		VALUES (?, 1, 1, 1, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(commandName, createdDate, createdHour) DO UPDATE SET
			totalCount = totalCount + 1, dailyCount = dailyCount + 1, hourlyCount = hourlyCount + 1, lastExecuted = CURRENT_TIMESTAMP
	`, commandName, now.Format("2006-01-02"), now.Hour())
	return err
}

func (db *Database) GetTotalCommandStats() map[string]int {
	rows, err := db.db.Query(`SELECT commandName, COUNT(*) FROM command_stats GROUP BY commandName ORDER BY COUNT(*) DESC`)
	if err != nil {
		return map[string]int{}
	}
	defer rows.Close()

	stats := map[string]int{}
	for rows.Next() {
		var cmd string
		var count int
		if rows.Scan(&cmd, &count) == nil {
			stats[cmd] = count
		}
	}
	return stats
}

func (db *Database) GetDailyCommandStats() map[string]int {
	today := time.Now().Format("2006-01-02")
	rows, err := db.db.Query(`SELECT commandName, SUM(dailyCount) FROM command_summary WHERE createdDate=? GROUP BY commandName ORDER BY SUM(dailyCount) DESC`, today)
	if err != nil {
		return map[string]int{}
	}
	defer rows.Close()

	stats := map[string]int{}
	for rows.Next() {
		var cmd string
		var count int
		if rows.Scan(&cmd, &count) == nil {
			stats[cmd] = count
		}
	}
	return stats
}

func (db *Database) GetHourlyCommandStats() map[string]int {
	now := time.Now()
	rows, err := db.db.Query(`SELECT commandName, hourlyCount FROM command_summary WHERE createdDate=? AND createdHour=? ORDER BY hourlyCount DESC`, now.Format("2006-01-02"), now.Hour())
	if err != nil {
		return map[string]int{}
	}
	defer rows.Close()

	stats := map[string]int{}
	for rows.Next() {
		var cmd string
		var count int
		if rows.Scan(&cmd, &count) == nil {
			stats[cmd] = count
		}
	}
	return stats
}

func (db *Database) GetTopCommands(limit int) []map[string]interface{} {
	rows, err := db.db.Query(`SELECT commandName, COUNT(*) as c FROM command_stats GROUP BY commandName ORDER BY c DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var cmd string
		var count int
		if rows.Scan(&cmd, &count) == nil {
			result = append(result, map[string]interface{}{"command": cmd, "count": count})
		}
	}
	return result
}

func (db *Database) GetTopUsers(limit int) []map[string]interface{} {
	rows, err := db.db.Query(`SELECT userId, COUNT(*) as c FROM command_stats GROUP BY userId ORDER BY c DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var uid string
		var count int
		if rows.Scan(&uid, &count) == nil {
			result = append(result, map[string]interface{}{"user": uid, "count": count})
		}
	}
	return result
}

func (db *Database) GetSystemStats() map[string]interface{} {
	var totalUsers, totalCmds, totalTx int64
	var totalBalance float64

	db.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&totalUsers)
	db.db.QueryRow("SELECT COUNT(*) FROM command_stats").Scan(&totalCmds)
	db.db.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&totalTx)
	db.db.QueryRow("SELECT COALESCE(SUM(balance),0) FROM users").Scan(&totalBalance)

	var activeUsers, onlineUsers int64
	db.db.QueryRow(`SELECT COUNT(*) FROM users WHERE lastSeen >= datetime('now','-1 day')`).Scan(&activeUsers)
	db.db.QueryRow(`SELECT COUNT(*) FROM users WHERE lastSeen >= datetime('now','-1 hour')`).Scan(&onlineUsers)

	avgBal := 0.0
	if totalUsers > 0 {
		avgBal = totalBalance / float64(totalUsers)
	}

	return map[string]interface{}{
		"totalUsers":        totalUsers,
		"activeUsers":       activeUsers,
		"onlineUsers":       onlineUsers,
		"totalCommands":     totalCmds,
		"totalTransactions": totalTx,
		"totalBalance":      fmt.Sprintf("$%.3f", totalBalance),
		"averageBalance":    fmt.Sprintf("$%.3f", avgBal),
	}
}

// GetTopGroupUsers mengambil statistik top user di suatu grup
func (db *Database) GetTopGroupUsers(groupID string, limit int) []GroupUserStat {
	rows, err := db.db.Query(`
		SELECT g.userId, COALESCE(u.name, 'Unknown'), g.messageCount, g.mediaCount, g.wordCount, g.lastActive 
		FROM group_stats g
		LEFT JOIN users u ON g.userId = u.id
		WHERE g.groupID = ? 
		ORDER BY g.messageCount DESC LIMIT ?`, groupID, limit)

	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []GroupUserStat
	for rows.Next() {
		var stat GroupUserStat
		if rows.Scan(&stat.UserID, &stat.Name, &stat.MessageCount, &stat.MediaCount, &stat.WordCount, &stat.LastActive) == nil {
			result = append(result, stat)
		}
	}
	return result
}

// Update AddGroupStat di src/database.go
func (db *Database) AddGroupStat(groupID, userID string, isMedia bool, wordCount int) {
	mediaInc := 0
	if isMedia {
		mediaInc = 1
	}

	// Ambil waktu saat pesan dikirim
	now := time.Now()
	currentDate := now.Format("2006-01-02")
	currentHour := now.Hour()

	// 1. Update Tabel Utama (Total Keseluruhan)
	go db.db.Exec(`
		INSERT INTO group_stats (groupID, userId, messageCount, mediaCount, wordCount, lastActive)
		VALUES (?, ?, 1, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(groupID, userId) DO UPDATE SET
			messageCount = messageCount + 1, mediaCount = mediaCount + ?,
			wordCount = wordCount + ?, lastActive = CURRENT_TIMESTAMP
	`, groupID, userID, mediaInc, wordCount, mediaInc, wordCount)

	// 2. Update Tabel Harian (Berdasarkan Tanggal & Jam)
	go db.db.Exec(`
		INSERT INTO group_stats_daily (groupID, userId, statDate, statHour, messageCount, mediaCount, wordCount)
		VALUES (?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(groupID, userId, statDate, statHour) DO UPDATE SET
			messageCount = messageCount + 1, mediaCount = mediaCount + ?, wordCount = wordCount + ?
	`, groupID, userID, currentDate, currentHour, mediaInc, wordCount, mediaInc, wordCount)
}

// Mengambil rekap user berdasarkan tanggal tertentu (YYYY-MM-DD)
func (db *Database) GetDailyGroupUsers(groupID string, date string, limit int) []GroupUserStat {
	rows, err := db.db.Query(`
		SELECT g.userId, COALESCE(u.name, 'Unknown'), SUM(g.messageCount), SUM(g.mediaCount), SUM(g.wordCount)
		FROM group_stats_daily g
		LEFT JOIN users u ON g.userId = u.id
		WHERE g.groupID = ? AND g.statDate = ?
		GROUP BY g.userId
		ORDER BY SUM(g.messageCount) DESC LIMIT ?`, groupID, date, limit)

	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []GroupUserStat
	for rows.Next() {
		var stat GroupUserStat
		if rows.Scan(&stat.UserID, &stat.Name, &stat.MessageCount, &stat.MediaCount, &stat.WordCount) == nil {
			result = append(result, stat)
		}
	}
	return result
}

// Mengambil jam paling sibuk (Peak Hour) di suatu grup pada tanggal tertentu
func (db *Database) GetPeakHourGroup(groupID string, date string) (int, int) {
	var peakHour, totalMsg int
	err := db.db.QueryRow(`
		SELECT statHour, SUM(messageCount) FROM group_stats_daily 
		WHERE groupID = ? AND statDate = ?
		GROUP BY statHour ORDER BY SUM(messageCount) DESC LIMIT 1
	`, groupID, date).Scan(&peakHour, &totalMsg)

	if err != nil {
		return -1, 0 // Jika tidak ada data
	}
	return peakHour, totalMsg
}

// GetHourlyGroupStats mengambil distribusi pesan per jam pada tanggal tertentu
func (db *Database) GetHourlyGroupStats(groupID string, date string) []HourlyStat {
	rows, err := db.db.Query(`
		SELECT statHour, SUM(messageCount) 
		FROM group_stats_daily 
		WHERE groupID = ? AND statDate = ?
		GROUP BY statHour 
		ORDER BY statHour ASC
	`, groupID, date)

	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []HourlyStat
	for rows.Next() {
		var stat HourlyStat
		if rows.Scan(&stat.Hour, &stat.Message) == nil {
			result = append(result, stat)
		}
	}
	return result
}

// GetMonthlyGroupStats mengambil akumulasi pesan per tanggal (1-31) pada bulan & tahun tertentu
func (db *Database) GetMonthlyGroupStats(groupID string, month time.Month, year int) []DailyStat {
	// Format filter string untuk SQLite (Contoh: "2026-05-%")
	datePrefix := fmt.Sprintf("%04d-%02d-%%", year, month)

	rows, err := db.db.Query(`
		SELECT CAST(strftime('%d', statDate) AS INTEGER), SUM(messageCount)
		FROM group_stats_daily
		WHERE groupID = ? AND statDate LIKE ?
		GROUP BY statDate
		ORDER BY statDate ASC
	`, groupID, datePrefix)

	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []DailyStat
	for rows.Next() {
		var stat DailyStat
		if rows.Scan(&stat.Day, &stat.Message) == nil {
			result = append(result, stat)
		}
	}
	return result
}
func (db *Database) Close() error {
	return db.db.Close()
}
