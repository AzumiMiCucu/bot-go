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
	"os"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
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
	// DIAGNOSTIK: pasang logger zerolog ke meowcaller. Default library = zerolog.Nop()
	// (senyap total), sehingga ALASAN PERSIS sebuah panggilan berakhir tak pernah
	// terlihat. Dengan logger ini, console akan mencetak penyebabnya — mis.
	//   "call rejected by server" error_code=...  (server WA menolak <offer>)
	//   "call terminated" reason=...              (server/lawan kirim <terminate>)
	//   "relay silent after allocate..."          (relay tak pernah membridge media)
	//   "media ended" err=...                     (handshake DTLS/relay gagal)
	// Level debug = logging di BOUNDARY saja (bukan per-frame; per-frame ada di trace),
	// jadi tidak membanjiri log. Set MEOW_CALL_DEBUG=0 untuk mematikan.
	callLog := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}).
		Level(zerolog.DebugLevel).
		With().Timestamp().Str("mod", "meowcaller").Logger()
	if os.Getenv("MEOW_CALL_DEBUG") == "0" {
		callLog = zerolog.Nop()
	}
	CallClient = meowcaller.NewClient(wa, meowcaller.WithLogger(callLog))

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
	var provide AudioProvider
	if audioPath != "" {
		// Validasi MURAH lebih dulu (ada file + ekstensi didukung) agar gagal cepat
		// SEBELUM menelepon. Decode penuh ditunda ke provider (jalan saat berdering).
		if err := quickValidateCallAudio(audioPath); err != nil {
			return "", "", err
		}
		provide = func(context.Context) (meowcaller.AudioSource, error) {
			return prepareCallAudio(audioPath)
		}
	}
	return StartCallProvider(ctx, target, provide, notify)
}

// AudioProvider menyiapkan AudioSource (mis. unduh + decode lagu). Dipanggil SEKALI
// dan DITUNGGU sampai selesai SEBELUM panggilan dimulai (lihat StartCallProvider).
type AudioProvider func(ctx context.Context) (meowcaller.AudioSource, error)

// StartCallProvider menelepon target lalu memutar audio yang disiapkan provide.
//
// POLA (terbukti first-call-work, lihat commit bd59b4a): CallClient.Call dipicu
// LEBIH DULU, dan provide() (DECODE + olah anti-kresek) berjalan di BACKGROUND
// menumpang waktu berdering; hasilnya ditunggu di OnReady (praktis sudah selesai
// saat lawan mengangkat). KUNCI: provide HANYA boleh men-DECODE audio yang BYTE-nya
// SUDAH ADA (file/memori) — TANPA I/O jaringan. Unduhan (mis. MP3 dari internet)
// WAJIB dilakukan pemanggil SAMPAI SELESAI sebelum StartCallProvider, sebab unduhan
// jaringan yang berjalan bersamaan dengan handshake relay UDP membuat relay tak
// pernah membridge → panggilan "tak terjawab" seketika. Decode (CPU) aman dijalankan
// saat berdering; unduhan (jaringan) TIDAK.
func StartCallProvider(ctx context.Context, target string, provide AudioProvider, notify func(string)) (string, string, error) {
	if CallClient == nil {
		return "", "", fmt.Errorf("subsistem panggilan belum diinisialisasi")
	}

	// Decode + olah audio di BACKGROUND sekarang juga (paralel dengan dering).
	var preparedSrc meowcaller.AudioSource
	var prepareErr error
	prepareDone := make(chan struct{})
	if provide != nil {
		go func() {
			preparedSrc, prepareErr = provide(ctx)
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
		if provide == nil {
			return
		}
		// Tunggu hasil decode background (praktis sudah selesai saat lawan mengangkat).
		<-prepareDone
		if prepareErr != nil {
			Print("[CALL] ⚠️ Gagal menyiapkan audio panggilan: %v", prepareErr)
			_ = call.Hangup()
			return
		}
		if preparedSrc == nil {
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

// StartCallMP3Download mengUNDUH byte MP3 SAMPAI SELESAI dulu, BARU menelepon —
// unduhan jaringan TIDAK boleh berjalan saat panggilan disiapkan (lihat
// StartCallProvider: itu memicu "tak terjawab"). Setelah byte di tangan, decode/olah
// anti-kresek diserahkan ke StartCallProvider (jalan di background saat berdering).
func StartCallMP3Download(ctx context.Context, target string, download func(context.Context) ([]byte, error), notify func(string)) (string, string, error) {
	// 1. Unduh PENUH dulu (jaringan), sebelum ada panggilan.
	data, err := download(ctx)
	if err != nil {
		return "", "", err
	}
	// 2. provide hanya men-DECODE byte yang sudah ada (tanpa jaringan) — aman saat dering.
	provide := func(context.Context) (meowcaller.AudioSource, error) {
		return prepareCallAudioFromMP3Bytes(data)
	}
	return StartCallProvider(ctx, target, provide, notify)
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
