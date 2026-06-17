package src

import (
	"sync"
)

// =================================================================
// PENGATURAN GRUP & DAFTAR TRUST (dengan cache in-memory)
// Dipakai oleh fitur anti-bot. Semua baca diutamakan dari cache agar
// tidak query DB tiap pesan masuk.
// =================================================================

var (
	antibotCache   = make(map[string]bool) // groupID -> antibot enabled
	antibotCacheMu sync.RWMutex
	antibotLoaded  bool

	trustCache   = make(map[string]map[string]bool) // groupID -> set(userID)
	trustCacheMu sync.RWMutex
	trustLoaded  bool
)

// loadAntibotCache memuat semua group_settings ke cache sekali saja.
func (db *Database) loadAntibotCache() {
	antibotCacheMu.Lock()
	defer antibotCacheMu.Unlock()
	if antibotLoaded {
		return
	}
	rows, err := db.db.Query("SELECT groupID, antibot FROM group_settings")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gid string
			var ab int
			if rows.Scan(&gid, &ab) == nil {
				antibotCache[gid] = ab == 1
			}
		}
	}
	antibotLoaded = true
}

// IsGroupAntibot mengembalikan status antibot suatu grup (dari cache).
func (db *Database) IsGroupAntibot(groupID string) bool {
	db.loadAntibotCache()
	antibotCacheMu.RLock()
	defer antibotCacheMu.RUnlock()
	return antibotCache[groupID]
}

// SetGroupAntibot mengaktifkan/menonaktifkan antibot untuk grup.
func (db *Database) SetGroupAntibot(groupID string, enabled bool) {
	val := 0
	if enabled {
		val = 1
	}
	db.db.Exec(`
		INSERT INTO group_settings (groupID, antibot, updatedAt) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(groupID) DO UPDATE SET antibot=?, updatedAt=CURRENT_TIMESTAMP
	`, groupID, val, val)

	antibotCacheMu.Lock()
	antibotCache[groupID] = enabled
	antibotCacheMu.Unlock()
}

// loadTrustCache memuat semua group_trust ke cache sekali saja.
func (db *Database) loadTrustCache() {
	trustCacheMu.Lock()
	defer trustCacheMu.Unlock()
	if trustLoaded {
		return
	}
	rows, err := db.db.Query("SELECT groupID, userID FROM group_trust")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gid, uid string
			if rows.Scan(&gid, &uid) == nil {
				if trustCache[gid] == nil {
					trustCache[gid] = make(map[string]bool)
				}
				trustCache[gid][uid] = true
			}
		}
	}
	trustLoaded = true
}

// IsTrusted mengecek apakah user termasuk daftar trust di grup.
func (db *Database) IsTrusted(groupID, userID string) bool {
	db.loadTrustCache()
	trustCacheMu.RLock()
	defer trustCacheMu.RUnlock()
	if set, ok := trustCache[groupID]; ok {
		return set[userID]
	}
	return false
}

// AddTrust menambahkan user ke daftar trust grup.
func (db *Database) AddTrust(groupID, userID string) {
	db.db.Exec("INSERT OR IGNORE INTO group_trust (groupID, userID) VALUES (?, ?)", groupID, userID)

	trustCacheMu.Lock()
	if trustCache[groupID] == nil {
		trustCache[groupID] = make(map[string]bool)
	}
	trustCache[groupID][userID] = true
	trustCacheMu.Unlock()
}

// RemoveTrust menghapus user dari daftar trust grup.
func (db *Database) RemoveTrust(groupID, userID string) {
	db.db.Exec("DELETE FROM group_trust WHERE groupID=? AND userID=?", groupID, userID)

	trustCacheMu.Lock()
	if set, ok := trustCache[groupID]; ok {
		delete(set, userID)
	}
	trustCacheMu.Unlock()
}

// GetTrustList mengembalikan daftar userID yang trusted di grup.
func (db *Database) GetTrustList(groupID string) []string {
	db.loadTrustCache()
	trustCacheMu.RLock()
	defer trustCacheMu.RUnlock()
	var out []string
	for uid := range trustCache[groupID] {
		out = append(out, uid)
	}
	return out
}
