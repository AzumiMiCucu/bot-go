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

	antilinkCache   = make(map[string]bool) // groupID -> antilink ON/OFF
	antilinkCacheMu sync.RWMutex
	antilinkLoaded  bool

	linksetCache   = make(map[string][]string) // groupID -> daftar pola link diblokir
	linksetCacheMu sync.RWMutex
	linksetLoaded  bool
)

// DefaultLinkPattern: pola default yang diblokir saat antilink ON tanpa custom = link grup WA.
const DefaultLinkPattern = "chat.whatsapp.com"

// loadAntilinkCache memuat status on/off antilink semua grup sekali saja.
func (db *Database) loadAntilinkCache() {
	antilinkCacheMu.Lock()
	defer antilinkCacheMu.Unlock()
	if antilinkLoaded {
		return
	}
	rows, err := db.db.Query("SELECT groupID, COALESCE(antilink,'') FROM group_settings")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gid, val string
			if rows.Scan(&gid, &val) == nil && val == "1" {
				antilinkCache[gid] = true
			}
		}
	}
	antilinkLoaded = true
}

// IsAntilinkOn mengembalikan status antilink grup (on/off).
func (db *Database) IsAntilinkOn(groupID string) bool {
	db.loadAntilinkCache()
	antilinkCacheMu.RLock()
	defer antilinkCacheMu.RUnlock()
	return antilinkCache[groupID]
}

// SetAntilinkOn menyalakan/mematikan antilink grup.
func (db *Database) SetAntilinkOn(groupID string, on bool) {
	val := ""
	if on {
		val = "1"
	}
	db.db.Exec(`
		INSERT INTO group_settings (groupID, antilink, updatedAt) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(groupID) DO UPDATE SET antilink=?, updatedAt=CURRENT_TIMESTAMP
	`, groupID, val, val)

	antilinkCacheMu.Lock()
	if on {
		antilinkCache[groupID] = true
	} else {
		delete(antilinkCache, groupID)
	}
	antilinkCacheMu.Unlock()
}

func (db *Database) loadLinksetCache() {
	linksetCacheMu.Lock()
	defer linksetCacheMu.Unlock()
	if linksetLoaded {
		return
	}
	rows, err := db.db.Query("SELECT groupID, pattern FROM group_linkset")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gid, pat string
			if rows.Scan(&gid, &pat) == nil {
				linksetCache[gid] = append(linksetCache[gid], pat)
			}
		}
	}
	linksetLoaded = true
}

// GetLinkPatterns mengembalikan daftar pola link yang diblokir grup.
// Bila kosong → default [chat.whatsapp.com] (link grup WA).
func (db *Database) GetLinkPatterns(groupID string) []string {
	db.loadLinksetCache()
	linksetCacheMu.RLock()
	pats := linksetCache[groupID]
	linksetCacheMu.RUnlock()
	if len(pats) == 0 {
		return []string{DefaultLinkPattern}
	}
	out := make([]string, len(pats))
	copy(out, pats)
	return out
}

// AddLinkPattern menambah pola link yang diblokir.
func (db *Database) AddLinkPattern(groupID, pattern string) {
	db.db.Exec("INSERT OR IGNORE INTO group_linkset (groupID, pattern) VALUES (?, ?)", groupID, pattern)
	linksetCacheMu.Lock()
	for _, p := range linksetCache[groupID] {
		if p == pattern {
			linksetCacheMu.Unlock()
			return
		}
	}
	linksetCache[groupID] = append(linksetCache[groupID], pattern)
	linksetCacheMu.Unlock()
}

// RemoveLinkPattern menghapus pola link dari daftar.
func (db *Database) RemoveLinkPattern(groupID, pattern string) {
	db.db.Exec("DELETE FROM group_linkset WHERE groupID=? AND pattern=?", groupID, pattern)
	linksetCacheMu.Lock()
	cur := linksetCache[groupID]
	out := cur[:0]
	for _, p := range cur {
		if p != pattern {
			out = append(out, p)
		}
	}
	linksetCache[groupID] = out
	linksetCacheMu.Unlock()
}

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
