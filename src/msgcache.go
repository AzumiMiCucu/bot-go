package src

import (
	"sync"
	"time"
)

// =================================================================
// LIVE DETECTION CACHE
// =================================================================
// Metadata pembeda bot (MessageSecret, DeviceListMetadata, BotMetadata, dll.)
// HANYA ada pada pesan saat tiba LIVE — WhatsApp membuangnya dari salinan pesan
// yang di-quote saat seseorang me-reply. Maka kita tangkap sinyal raw tiap pesan
// masuk (async, off hot-path), dikunci message-ID, agar cekbot yang me-reply
// sebuah pesan bisa membaca analisis ASLI-nya, bukan tebakan dari quote.
//
// Bounded oleh TTL + pembersihan berkala (pola sama seperti reply_router).

type capturedDetection struct {
	det       BotDetectionResult
	expiresAt time.Time
}

var (
	detCache   = make(map[string]capturedDetection)
	detCacheMu sync.RWMutex
)

const detCacheTTL = 30 * time.Minute

func init() {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		for range t.C {
			now := time.Now()
			detCacheMu.Lock()
			for id, c := range detCache {
				if now.After(c.expiresAt) {
					delete(detCache, id)
				}
			}
			detCacheMu.Unlock()
		}
	}()
}

// CaptureDetection menyimpan hasil analisis raw sebuah pesan (saat tiba live),
// dikunci message-ID. Dipanggil ASINKRON dari handler agar tak menambah latensi.
func CaptureDetection(det BotDetectionResult) {
	if det.MessageID == "" {
		return
	}
	detCacheMu.Lock()
	detCache[det.MessageID] = capturedDetection{det: det, expiresAt: time.Now().Add(detCacheTTL)}
	detCacheMu.Unlock()
}

// LookupDetection mengambil analisis raw yang ditangkap live untuk sebuah
// message-ID (mis. StanzaID pesan yang di-reply). Mengembalikan (det, false)
// bila tak ada / sudah kedaluwarsa → pemanggil fallback ke analisis quote.
func LookupDetection(msgID string) (BotDetectionResult, bool) {
	detCacheMu.RLock()
	c, ok := detCache[msgID]
	detCacheMu.RUnlock()
	if !ok || time.Now().After(c.expiresAt) {
		return BotDetectionResult{}, false
	}
	// Verdict tersimpan dihitung saat pesan tiba; reputasi akun bisa berubah sejak itu
	// (mis. akun baru ketahuan bot dari pesan-pesan setelahnya). Terapkan ulang agar
	// cekbot yang me-reply pesan lama tetap mencerminkan reputasi terbaru.
	det := c.det
	applyAccountReputation(&det)
	return det, true
}
