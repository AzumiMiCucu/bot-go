// src/caller.go
//
// Subsistem PANGGILAN (VoIP 1:1) berbasis github.com/purpshell/meowcaller.
// meowcaller membungkus *whatsmeow.Client dan mengurus seluruh siklus call
// (signaling, keying, relay, media) di balik API kecil. Modul ini menyimpan
// satu Client global, menyajikan helper sederhana untuk command (StartCall,
// HangupCall, dst) sehingga package commands tak perlu mengimpor meowcaller.
package src

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

var (
	// CallClient adalah entry point meowcaller (nil bila belum di-init).
	CallClient *meowcaller.Client
	callWA     *whatsmeow.Client

	callMu      sync.Mutex
	activeCalls = make(map[string]*meowcaller.Call) // call-id → live call
)

// InitCaller membungkus client whatsmeow dengan stack call meowcaller dan
// memasang handler panggilan masuk. WAJIB dipanggil SEBELUM client.Connect()
// agar intersepsi node <call>/<ack> level-rendah sudah terpasang sebelum
// receive-loop berjalan.
func InitCaller(wa *whatsmeow.Client) {
	callWA = wa
	CallClient = meowcaller.NewClient(wa)

	CallClient.OnIncomingCall(func(c *meowcaller.Call) {
		peer := c.Peer().String()

		// Anti-call: tolak otomatis bila diaktifkan owner.
		if AppConfig != nil && AppConfig.AntiCall {
			if err := c.Reject(); err != nil {
				Print("[CALL] ⚠️ Gagal menolak panggilan dari %s: %v", peer, err)
			} else {
				Print("[CALL] 🚫 Panggilan masuk dari %s ditolak (anti-call aktif).", peer)
			}
			notifyOwner(fmt.Sprintf("🚫 *ANTI-CALL*\nMenolak panggilan masuk dari:\n%s", peer))
			return
		}

		Print("[CALL] 📞 Panggilan masuk dari %s (anti-call nonaktif, dibiarkan).", peer)
	})

	Print("[CALL] ☎️ Subsistem panggilan (meowcaller) siap.")
}

// StartCall menelepon target (nomor telepon, JID telepon, atau @lid JID).
// Bila audioPath terisi, file audio diputar otomatis begitu media tersambung
// lalu call ditutup setelah audio selesai. notify (boleh nil) dipanggil tiap
// perubahan fase agar command bisa melaporkan progres ke chat.
// Mengembalikan call-id, JID peer, dan error.
func StartCall(ctx context.Context, target, audioPath string, notify func(string)) (string, string, error) {
	if CallClient == nil {
		return "", "", fmt.Errorf("subsistem panggilan belum diinisialisasi")
	}

	// Validasi MURAH lebih dulu (ada file + ekstensi didukung) agar gagal cepat
	// sebelum menelepon. Decode penuh sengaja DITUNDA & dijalankan asinkron di
	// bawah agar inisiasi panggilan tetap secepat mungkin.
	if audioPath != "" {
		if err := quickValidateCallAudio(audioPath); err != nil {
			return "", "", err
		}
	}

	// Mulai menyiapkan (decode + olah) audio di BACKGROUND sekarang juga. Proses
	// ini menumpang waktu berdering sehingga latensi decode tak terasa: saat lawan
	// mengangkat (OnReady), frame sudah siap di memori.
	var preparedSrc meowcaller.AudioSource
	var prepareErr error
	prepareDone := make(chan struct{})
	if audioPath != "" {
		go func() {
			preparedSrc, prepareErr = prepareCallAudio(audioPath)
			close(prepareDone)
		}()
	} else {
		close(prepareDone)
	}

	call, err := CallClient.Call(ctx, target)
	if err != nil {
		return "", "", err
	}

	callID := call.ID()
	peer := call.Peer().String()

	callMu.Lock()
	activeCalls[callID] = call
	callMu.Unlock()

	if notify != nil {
		call.OnStateChange(func(p meowcaller.CallPhase) {
			notify(phaseLabel(p))
		})
	}

	call.OnReady(func() {
		if audioPath == "" {
			return
		}
		// Tunggu hasil decode background (praktis sudah selesai saat lawan mengangkat).
		<-prepareDone
		if prepareErr != nil {
			Print("[CALL] ⚠️ Gagal menyiapkan audio %q: %v", audioPath, prepareErr)
			return
		}
		player := call.Play(preparedSrc)
		// Tutup call begitu audio habis (one-shot greeting/voicemail).
		player.OnFinish(func() { _ = call.Hangup() })
	})

	call.OnEnd(func(reason string) {
		callMu.Lock()
		delete(activeCalls, callID)
		callMu.Unlock()
		if notify != nil {
			notify("ended:" + reason)
		}
	})

	return callID, peer, nil
}

// HangupCall menutup satu panggilan aktif berdasarkan call-id.
func HangupCall(callID string) bool {
	callMu.Lock()
	call, ok := activeCalls[callID]
	callMu.Unlock()
	if !ok {
		return false
	}
	_ = call.Hangup()
	return true
}

// HangupAllCalls menutup semua panggilan aktif (dipakai saat disconnect/shutdown).
// Mengembalikan jumlah panggilan yang ditutup.
func HangupAllCalls() int {
	callMu.Lock()
	calls := activeCalls
	activeCalls = make(map[string]*meowcaller.Call)
	callMu.Unlock()
	for _, c := range calls {
		_ = c.Hangup()
	}
	return len(calls)
}

// ActiveCallCount mengembalikan jumlah panggilan yang sedang berlangsung.
func ActiveCallCount() int {
	callMu.Lock()
	defer callMu.Unlock()
	return len(activeCalls)
}

// phaseLabel memetakan fase call ke label ringkas (meowcaller tak mengekspor String()).
func phaseLabel(p meowcaller.CallPhase) string {
	switch p {
	case meowcaller.CallPhaseCalling:
		return "calling"
	case meowcaller.CallPhaseRinging:
		return "ringing"
	case meowcaller.CallPhaseConnecting:
		return "connecting"
	case meowcaller.CallPhaseActive:
		return "active"
	case meowcaller.CallPhaseEnded:
		return "ended"
	default:
		return "idle"
	}
}

// notifyOwner mengirim notifikasi teks singkat ke owner (best-effort, async-safe).
func notifyOwner(text string) {
	if callWA == nil || AppConfig == nil || AppConfig.OwnerNumber == "" {
		return
	}
	ownerJID := types.NewJID(AppConfig.OwnerNumber, types.DefaultUserServer)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = callWA.SendMessage(ctx, ownerJID, &waProto.Message{
			Conversation: proto.String(text),
		}, AndroidExtra())
	}()
}
