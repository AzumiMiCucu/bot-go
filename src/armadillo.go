package src

import (
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// =================================================================
// ARMADILLO / FB MESSAGE — pesan interop Meta (Messenger/Instagram ↔ WhatsApp).
// =================================================================
// whatsmeow SUDAH mendekode transport Armadillo & mem-dispatch *events.FBMessage
// secara internal (handleDecryptedArmadillo → dispatchEvent). Tapi hampir tak
// ada bot yang MENANGKAP-nya — ini jalur pesan Meta lintas-aplikasi yang sangat
// langka (muncul saat user Messenger/Instagram mengirim ke nomor WA lewat
// interop Meta). Di sini kita tangkap, ringkas, dan simpan yang terakhir agar
// bisa dilihat lewat command `armadillo`.
// =================================================================

// FBCapture = ringkasan satu FBMessage yang tertangkap.
type FBCapture struct {
	Time      time.Time
	Chat      string
	Sender    string
	Kind      string // FB consumer / IG / business / dll (tipe Go konkret)
	Retry     int
	Summary   string
}

var (
	fbMu       sync.RWMutex
	fbLast     []FBCapture
	fbTotal    int
	fbMaxKeep  = 20
	fbVerbose  = true // cetak ke console saat tertangkap
)

// HandleFBMessage dipanggil dari event handler utama untuk tiap *events.FBMessage.
func HandleFBMessage(client *whatsmeow.Client, evt *events.FBMessage) {
	if evt == nil {
		return
	}

	kind := "unknown"
	if evt.Message != nil {
		kind = fmt.Sprintf("%T", evt.Message)
	} else if evt.IGTransport != nil {
		kind = "instagram-transport"
	} else if evt.FBApplication != nil {
		kind = "fb-application"
	}

	summary := "(payload non-teks / tak didukung)"
	if evt.Message != nil {
		// MessageApplicationSub punya String() dari protobuf → ringkas amannya.
		if s := fmt.Sprintf("%v", evt.Message); s != "" && len(s) < 500 {
			summary = s
		}
	}

	cap := FBCapture{
		Time:    time.Now(),
		Chat:    evt.Info.Chat.String(),
		Sender:  evt.Info.Sender.String(),
		Kind:    kind,
		Retry:   evt.RetryCount,
		Summary: summary,
	}

	fbMu.Lock()
	fbTotal++
	fbLast = append(fbLast, cap)
	if len(fbLast) > fbMaxKeep {
		fbLast = fbLast[len(fbLast)-fbMaxKeep:]
	}
	verbose := fbVerbose
	fbMu.Unlock()

	if verbose {
		fmt.Printf("[ARMADILLO] 🦔 FBMessage dari %s (chat %s) tipe=%s retry=%d\n",
			cap.Sender, cap.Chat, cap.Kind, cap.Retry)
	}
}

// FBLastCaptures mengembalikan salinan N capture terakhir (terbaru dulu) + total.
func FBLastCaptures(n int) ([]FBCapture, int) {
	fbMu.RLock()
	defer fbMu.RUnlock()
	if n <= 0 || n > len(fbLast) {
		n = len(fbLast)
	}
	out := make([]FBCapture, 0, n)
	for i := len(fbLast) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, fbLast[i])
	}
	return out, fbTotal
}

// FBSetVerbose menyalakan/mematikan log console.
func FBSetVerbose(v bool) {
	fbMu.Lock()
	fbVerbose = v
	fbMu.Unlock()
}
