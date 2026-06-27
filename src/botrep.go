package src

import (
	"sync"
	"time"
)

// =================================================================
// REPUTASI BOT PER-AKUN (memori antar-pesan)
// =================================================================
// "BATAS TAK TERPECAHKAN": bot companion ber-ID web/iOS yang kirim pesan BIASA
// IDENTIK dengan WA Web asli secara metadata SATU pesan. Tak ada pembeda per-pesan.
// Solusinya = MEMORI antar-pesan: sekali sebuah AKUN menunjukkan sinyal bot, vonis
// itu MENEMPEL ke nomornya (TTL) dan MENULAR ke pesan berikutnya — termasuk pesan
// biasa yang kalau berdiri sendiri tak terbedakan.
//
// Dua pemicu penandaan (lihat CaptureReputation di botdetect.go):
//  1. Verdict bot DEFINITIF (interaktif / BAE* / HOSTED / server-bot / android
//     reply-tanpa-quotedType) → langsung tandai akun = bot.
//  2. Pola "reply tanpa quotedType" BERULANG pada companion (web/iOS sekalipun).
//     Klien WA asli SESEKALI bisa begitu, tapi BOT command melakukannya di TIAP
//     balasan → setelah rnqtBotThreshold pesan rnqt → tandai akun = bot.
//
// READ (applyAccountReputation) dipakai di semua jalur deteksi (live/quote/cache);
// WRITE (CaptureReputation) HANYA dari handler, sekali per pesan, agar hitungan rnqt
// tak ganda (antibot & cekbot juga memanggil DetectBot). Device-0 (HP utama) tak
// pernah tersentuh — gate r.IsSecondary + cek primary di classifyVerdict mendahului.

const (
	botRepTTL        = 6 * time.Hour
	rnqtBotThreshold = 3 // jumlah pesan reply-tanpa-quotedType (companion) → vonis bot
)

type botRepEntry struct {
	isBot     bool
	reason    string
	rnqtCount int
	expiresAt time.Time
}

var (
	botRepStore = make(map[string]*botRepEntry)
	botRepMu    sync.Mutex
)

func init() {
	go func() {
		t := time.NewTicker(15 * time.Minute)
		for range t.C {
			now := time.Now()
			botRepMu.Lock()
			for k, e := range botRepStore {
				if now.After(e.expiresAt) {
					delete(botRepStore, k)
				}
			}
			botRepMu.Unlock()
		}
	}()
}

// MarkAccountBot menandai sebuah akun (nomor) sebagai bot & me-refresh TTL.
func MarkAccountBot(user, reason string) {
	if user == "" {
		return
	}
	botRepMu.Lock()
	defer botRepMu.Unlock()
	e := botRepStore[user]
	if e == nil {
		e = &botRepEntry{}
		botRepStore[user] = e
	}
	if !e.isBot {
		e.isBot = true
		e.reason = reason
	}
	e.expiresAt = time.Now().Add(botRepTTL)
}

// NoteReplyNoQuotedType mencatat satu pesan "reply tanpa quotedType" dari akun ini.
// Setelah rnqtBotThreshold pesan → akun dianggap bot. Me-refresh TTL tiap pencatatan.
func NoteReplyNoQuotedType(user string) {
	if user == "" {
		return
	}
	botRepMu.Lock()
	defer botRepMu.Unlock()
	e := botRepStore[user]
	if e == nil {
		e = &botRepEntry{}
		botRepStore[user] = e
	}
	e.expiresAt = time.Now().Add(botRepTTL)
	if e.isBot {
		return
	}
	e.rnqtCount++
	if e.rnqtCount >= rnqtBotThreshold {
		e.isBot = true
		e.reason = "pola reply tanpa quotedType berulang (companion)"
	}
}

// KnownBotAccount: apakah akun ini sudah dikenal bot (dalam TTL).
func KnownBotAccount(user string) (bool, string) {
	if user == "" {
		return false, ""
	}
	botRepMu.Lock()
	defer botRepMu.Unlock()
	e := botRepStore[user]
	if e == nil || time.Now().After(e.expiresAt) {
		return false, ""
	}
	return e.isBot, e.reason
}

// applyAccountReputation MENAIKKAN verdict ke BOT bila akun (companion) sudah dikenal
// bot dari pesan-pesan sebelumnya. Hanya untuk companion (device != 0) & tak pernah
// menurunkan verdict. Dipakai di akhir setiap jalur deteksi (live/quote/cache).
func applyAccountReputation(r *BotDetectionResult) {
	if r == nil || r.Verdict == VerdictBot || !r.IsSecondary {
		return
	}
	if isBot, reason := KnownBotAccount(r.SenderUser); isBot {
		r.Verdict = VerdictBot
		r.DefinitiveBot = true
		// Perjelas bahwa vonis ini dari REPUTASI akun (pesan sekarang bisa saja biasa,
		// mis. VN) — bukan karena pesan ini sendiri interaktif/baileys.
		if reason != "" {
			r.VerdictReason = "akun dikenal bot — " + reason
		} else {
			r.VerdictReason = "akun sebelumnya terdeteksi bot"
		}
	}
}

// CaptureReputation MENULIS reputasi dari hasil deteksi LIVE sebuah pesan. Dipanggil
// HANYA dari handler (sekali per pesan) agar hitungan rnqt tidak ganda. Companion
// saja; device-0 diabaikan.
func CaptureReputation(r BotDetectionResult) {
	if r.SenderUser == "" || !r.IsSecondary {
		return
	}
	if r.DefinitiveBot {
		MarkAccountBot(r.SenderUser, r.VerdictReason)
		return
	}
	if r.Struct.ReplyNoQuotedType {
		NoteReplyNoQuotedType(r.SenderUser)
	}
}
