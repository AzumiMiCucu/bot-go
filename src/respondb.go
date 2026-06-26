package src

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// =================================================================
// RESPON STORE — database/respon.db (terpisah dari bot.db & msg.db)
// =================================================================
// Fitur "auto-respon": bot membalas otomatis suatu pesan berdasarkan KEYWORD
// yang di-set owner. Balasan bisa TEKS atau MEDIA (gambar/video/audio/stiker).
// Media disimpan sebagai BLOB di respon.db agar mandiri (tak bergantung file).
//
// Metadata keyword di-cache di memori untuk pencocokan CEPAT tiap pesan masuk
// (tanpa query DB); byte media hanya diambil dari DB SAAT keyword terpicu.
// =================================================================

// Tipe balasan yang didukung.
const (
	ResponText    = "text"
	ResponImage   = "image"
	ResponVideo   = "video"
	ResponAudio   = "audio"
	ResponSticker = "sticker"
)

// Tipe pencocokan keyword terhadap teks pesan masuk.
const (
	MatchExact    = "exact"    // teks pesan SAMA PERSIS dengan keyword
	MatchContains = "contains" // teks pesan MENGANDUNG keyword
)

// ResponMaxMediaBytes membatasi ukuran media yang boleh disimpan (hindari DB membengkak).
const ResponMaxMediaBytes = 30 * 1024 * 1024 // 30 MB

// Respon = satu aturan auto-respon (tanpa byte media — itu diambil terpisah).
type Respon struct {
	Keyword   string
	MatchType string
	Type      string // text|image|video|audio|sticker
	Text      string // isi teks / caption media
	Mimetype  string
	HasMedia  bool
	CreatedBy string
	CreatedAt int64
}

var (
	responDB    *sql.DB
	responMu    sync.RWMutex
	responCache = map[string]Respon{} // keyword(lower) -> metadata
)

// InitResponDB membuka database/respon.db, membuat tabel, dan memuat cache keyword.
// Dipanggil sekali dari main (setelah InitDatabase).
func InitResponDB() error {
	db, err := sql.Open("sqlite3", "database/respon.db?mode=rwc&cache=shared&_journal=wal")
	if err != nil {
		return fmt.Errorf("buka respon.db: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)
	db.Exec("PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA temp_store = MEMORY;")

	schema := `
	CREATE TABLE IF NOT EXISTS responses (
		keyword     TEXT PRIMARY KEY,   -- selalu lowercase
		match_type  TEXT DEFAULT 'exact',
		resp_type   TEXT DEFAULT 'text',
		text        TEXT DEFAULT '',
		media       BLOB,
		mimetype    TEXT DEFAULT '',
		created_by  TEXT DEFAULT '',
		created_at  INTEGER DEFAULT 0
	);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("buat tabel respon.db: %w", err)
	}
	responDB = db
	reloadResponCache()
	fmt.Printf("[DB] respon.db (auto-respon) berhasil diinisialisasi — %d keyword dimuat\n", len(responCache))
	return nil
}

// reloadResponCache memuat ulang seluruh metadata keyword (tanpa BLOB) ke cache.
func reloadResponCache() {
	if responDB == nil {
		return
	}
	rows, err := responDB.Query(`SELECT keyword, match_type, resp_type, text, mimetype,
		(media IS NOT NULL AND length(media) > 0), created_by, created_at FROM responses`)
	if err != nil {
		return
	}
	defer rows.Close()

	next := make(map[string]Respon)
	for rows.Next() {
		var r Respon
		if err := rows.Scan(&r.Keyword, &r.MatchType, &r.Type, &r.Text, &r.Mimetype,
			&r.HasMedia, &r.CreatedBy, &r.CreatedAt); err == nil {
			next[r.Keyword] = r
		}
	}
	responMu.Lock()
	responCache = next
	responMu.Unlock()
}

// AddRespon menyimpan / menimpa (upsert) sebuah aturan respon. media boleh nil
// (balasan teks). keyword di-normalkan ke lowercase + trim.
func AddRespon(keyword, matchType, respType, text, mimetype string, media []byte, createdBy string) error {
	if responDB == nil {
		return fmt.Errorf("respon.db belum diinisialisasi")
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return fmt.Errorf("keyword kosong")
	}
	if len(media) > ResponMaxMediaBytes {
		return fmt.Errorf("media terlalu besar (%d byte, maks %d)", len(media), ResponMaxMediaBytes)
	}
	if matchType != MatchContains {
		matchType = MatchExact
	}
	var blob interface{}
	if len(media) > 0 {
		blob = media
	}
	_, err := responDB.Exec(`INSERT INTO responses
		(keyword, match_type, resp_type, text, media, mimetype, created_by, created_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(keyword) DO UPDATE SET
			match_type=excluded.match_type, resp_type=excluded.resp_type,
			text=excluded.text, media=excluded.media, mimetype=excluded.mimetype,
			created_by=excluded.created_by, created_at=excluded.created_at`,
		keyword, matchType, respType, text, blob, mimetype, createdBy, time.Now().Unix())
	if err != nil {
		return err
	}
	reloadResponCache()
	return nil
}

// DelRespon menghapus aturan respon. Mengembalikan true bila ada yang terhapus.
func DelRespon(keyword string) (bool, error) {
	if responDB == nil {
		return false, fmt.Errorf("respon.db belum diinisialisasi")
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	res, err := responDB.Exec("DELETE FROM responses WHERE keyword = ?", keyword)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		reloadResponCache()
	}
	return n > 0, nil
}

// GetRespon mengembalikan metadata satu keyword (tanpa byte media) dari cache.
func GetRespon(keyword string) (Respon, bool) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	responMu.RLock()
	r, ok := responCache[keyword]
	responMu.RUnlock()
	return r, ok
}

// ListRespon mengembalikan semua aturan respon (metadata saja), urut keyword.
func ListRespon() []Respon {
	responMu.RLock()
	out := make([]Respon, 0, len(responCache))
	for _, r := range responCache {
		out = append(out, r)
	}
	responMu.RUnlock()
	// urut sederhana berdasarkan keyword
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Keyword < out[i].Keyword {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// GetResponMedia mengambil byte media sebuah keyword dari DB (lazy, hanya saat terpicu).
func GetResponMedia(keyword string) ([]byte, error) {
	if responDB == nil {
		return nil, fmt.Errorf("respon.db belum diinisialisasi")
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	var media []byte
	err := responDB.QueryRow("SELECT media FROM responses WHERE keyword = ?", keyword).Scan(&media)
	if err != nil {
		return nil, err
	}
	return media, nil
}

// MatchRespon mencari aturan respon yang cocok untuk teks pesan masuk. Cek EXACT
// dulu (paling spesifik), lalu CONTAINS. Mengembalikan (respon, true) bila cocok.
func MatchRespon(text string) (Respon, bool) {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return Respon{}, false
	}
	responMu.RLock()
	defer responMu.RUnlock()

	// 1) exact — langsung lookup map.
	if r, ok := responCache[t]; ok && r.MatchType == MatchExact {
		return r, true
	}
	// 2) contains — iterasi keyword bertipe contains.
	for _, r := range responCache {
		if r.MatchType == MatchContains && r.Keyword != "" && strings.Contains(t, r.Keyword) {
			return r, true
		}
	}
	return Respon{}, false
}

// ResponCount mengembalikan jumlah aturan respon terpasang.
func ResponCount() int {
	responMu.RLock()
	defer responMu.RUnlock()
	return len(responCache)
}

// CloseResponDB menutup koneksi respon.db (dipanggil saat shutdown).
func CloseResponDB() error {
	if responDB == nil {
		return nil
	}
	return responDB.Close()
}
