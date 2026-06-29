package src

import (
	"fmt"
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

	// grpmode: scope self/public PER-GRUP. DEFAULT GRUP = SELF — jadi cache hanya
	// menyimpan nilai EKSPLISIT ("self"/"public"); grup tanpa entri dianggap SELF.
	// Dibutuhkan tri-state (unset/self/public) karena default kini self, beda dari
	// kolom lama `selfmode` (DEFAULT 0) yang tak bisa membedakan unset vs public.
	grpmodeCache   = make(map[string]string) // groupID -> "self" | "public"
	grpmodeCacheMu sync.RWMutex
	grpmodeLoaded  bool
)

// loadGrpModeCache memuat mode self/public eksplisit tiap grup sekali saja.
func (db *Database) loadGrpModeCache() {
	grpmodeCacheMu.Lock()
	defer grpmodeCacheMu.Unlock()
	if grpmodeLoaded {
		return
	}
	rows, err := db.db.Query("SELECT groupID, COALESCE(grpmode,'') FROM group_settings")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gid, gm string
			if rows.Scan(&gid, &gm) == nil && gm != "" {
				grpmodeCache[gid] = gm
			}
		}
	}
	grpmodeLoaded = true
}

// IsGroupSelf mengembalikan true bila grup dalam mode SELF (hanya owner dilayani).
// DEFAULT GRUP = SELF: grup hanya PUBLIC bila owner men-set-nya eksplisit.
func (db *Database) IsGroupSelf(groupID string) bool {
	db.loadGrpModeCache()
	grpmodeCacheMu.RLock()
	defer grpmodeCacheMu.RUnlock()
	return grpmodeCache[groupID] != "public"
}

// SetGroupSelf mengatur mode self (true) / public (false) untuk grup (eksplisit).
func (db *Database) SetGroupSelf(groupID string, self bool) {
	mode := "self"
	if !self {
		mode = "public"
	}
	db.db.Exec(`
		INSERT INTO group_settings (groupID, grpmode, updatedAt) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(groupID) DO UPDATE SET grpmode=?, updatedAt=CURRENT_TIMESTAMP
	`, groupID, mode, mode)

	grpmodeCacheMu.Lock()
	grpmodeCache[groupID] = mode
	grpmodeCacheMu.Unlock()
}

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

// GetLinkPatterns mengembalikan daftar pola link yang diblokir grup. Link UNDANGAN
// GRUP WA (chat.whatsapp.com) SELALU diblokir — itu tujuan inti antilink — lalu
// ditambah pola custom grup. Dulu menambah pola custom diam-diam MEMBUANG default
// ini (bug): grup yang menambah mis. "tiktok.com" jadi tak lagi memblokir link grup.
func (db *Database) GetLinkPatterns(groupID string) []string {
	db.loadLinksetCache()
	linksetCacheMu.RLock()
	pats := linksetCache[groupID]
	linksetCacheMu.RUnlock()

	out := make([]string, 0, len(pats)+1)
	out = append(out, DefaultLinkPattern) // selalu blokir link undangan grup WA
	for _, p := range pats {
		if p != "" && p != DefaultLinkPattern { // hindari duplikat default
			out = append(out, p)
		}
	}
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

// =================================================================
// WELCOME / GOODBYE (sambutan & perpisahan grup)
// =================================================================

// GreetConfig: konfigurasi satu jenis sapaan (welcome / goodbye) per grup.
type GreetConfig struct {
	On        bool
	Text      string
	Type      string // text | image | video | sticker
	MediaPath string // path file media (kosong bila type=text)
}

var (
	// greetCache[kind][groupID] -> GreetConfig. kind = "welcome" / "goodbye".
	greetCache   = map[string]map[string]GreetConfig{"welcome": {}, "goodbye": {}}
	greetCacheMu sync.RWMutex
	greetLoaded  bool
)

func (db *Database) loadGreetCache() {
	greetCacheMu.Lock()
	defer greetCacheMu.Unlock()
	if greetLoaded {
		return
	}
	rows, err := db.db.Query(`SELECT groupID,
		COALESCE(welcome,0), COALESCE(welcomeText,''), COALESCE(welcomeType,'text'), COALESCE(welcomeMedia,''),
		COALESCE(goodbye,0), COALESCE(goodbyeText,''), COALESCE(goodbyeType,'text'), COALESCE(goodbyeMedia,'')
		FROM group_settings`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gid string
			var won, gon int
			var wt, wty, wm, gt, gty, gm string
			if rows.Scan(&gid, &won, &wt, &wty, &wm, &gon, &gt, &gty, &gm) == nil {
				greetCache["welcome"][gid] = GreetConfig{On: won == 1, Text: wt, Type: wty, MediaPath: wm}
				greetCache["goodbye"][gid] = GreetConfig{On: gon == 1, Text: gt, Type: gty, MediaPath: gm}
			}
		}
	}
	greetLoaded = true
}

func greetCols(kind string) (onCol, textCol, typeCol, mediaCol string) {
	if kind == "goodbye" {
		return "goodbye", "goodbyeText", "goodbyeType", "goodbyeMedia"
	}
	return "welcome", "welcomeText", "welcomeType", "welcomeMedia"
}

// GetGreet mengembalikan konfigurasi welcome/goodbye sebuah grup (dari cache).
func (db *Database) GetGreet(groupID, kind string) GreetConfig {
	db.loadGreetCache()
	greetCacheMu.RLock()
	defer greetCacheMu.RUnlock()
	if m, ok := greetCache[kind]; ok {
		return m[groupID]
	}
	return GreetConfig{}
}

func (db *Database) greetUpdate(groupID, kind, col string, val interface{}, apply func(*GreetConfig)) {
	db.loadGreetCache()
	q := fmt.Sprintf(`INSERT INTO group_settings (groupID, %s, updatedAt) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(groupID) DO UPDATE SET %s=?, updatedAt=CURRENT_TIMESTAMP`, col, col)
	db.db.Exec(q, groupID, val, val)

	greetCacheMu.Lock()
	cfg := greetCache[kind][groupID]
	apply(&cfg)
	greetCache[kind][groupID] = cfg
	greetCacheMu.Unlock()
}

// SetGreetOn menyalakan/mematikan welcome/goodbye.
func (db *Database) SetGreetOn(groupID, kind string, on bool) {
	onCol, _, _, _ := greetCols(kind)
	v := 0
	if on {
		v = 1
	}
	db.greetUpdate(groupID, kind, onCol, v, func(c *GreetConfig) { c.On = on })
}

// SetGreetText mengatur teks/caption sapaan.
func (db *Database) SetGreetText(groupID, kind, text string) {
	_, textCol, _, _ := greetCols(kind)
	db.greetUpdate(groupID, kind, textCol, text, func(c *GreetConfig) { c.Text = text })
}

// SetGreetMedia mengatur jenis + path media sapaan (image/video/sticker).
func (db *Database) SetGreetMedia(groupID, kind, mtype, path string) {
	_, _, typeCol, mediaCol := greetCols(kind)
	db.greetUpdate(groupID, kind, typeCol, mtype, func(c *GreetConfig) { c.Type = mtype })
	db.greetUpdate(groupID, kind, mediaCol, path, func(c *GreetConfig) { c.MediaPath = path })
}

// ClearGreetMedia menghapus media → kembali ke jenis teks.
func (db *Database) ClearGreetMedia(groupID, kind string) {
	db.SetGreetMedia(groupID, kind, "text", "")
}
