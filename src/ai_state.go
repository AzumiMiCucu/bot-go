package src

import (
	"sync"
	"time"
)

// =================================================================
// AI CHAT STATE — penyimpan state percakapan asisten AI (per provider+user).
// =================================================================
// Tiap provider (gemini/chatgpt/copilot) menyimpan state percakapannya sebagai
// string opaque (base64 JSON) agar konteks nyambung antar pesan. State disimpan
// DI MEMORI dengan TTL — cukup untuk sesi aktif; tak perlu persist ke disk.
// Reset (mulai percakapan baru) cukup menghapus entri.
// =================================================================

const aiStateTTL = 2 * time.Hour

type aiSession struct {
	state   string
	updated time.Time
}

var (
	aiMu       sync.Mutex
	aiSessions = map[string]aiSession{} // key = "provider|user"
)

func aiKey(provider, user string) string { return provider + "|" + user }

// GetAIState mengembalikan state percakapan tersimpan (kosong bila tak ada / kadaluarsa).
func GetAIState(provider, user string) string {
	aiMu.Lock()
	defer aiMu.Unlock()
	k := aiKey(provider, user)
	s, ok := aiSessions[k]
	if !ok {
		return ""
	}
	if time.Since(s.updated) > aiStateTTL {
		delete(aiSessions, k)
		return ""
	}
	return s.state
}

// SetAIState menyimpan/memperbarui state percakapan.
func SetAIState(provider, user, state string) {
	if state == "" {
		return
	}
	aiMu.Lock()
	defer aiMu.Unlock()
	aiSessions[aiKey(provider, user)] = aiSession{state: state, updated: time.Now()}
}

// ResetAIState menghapus state percakapan (mulai dari awal).
func ResetAIState(provider, user string) {
	aiMu.Lock()
	defer aiMu.Unlock()
	delete(aiSessions, aiKey(provider, user))
}
