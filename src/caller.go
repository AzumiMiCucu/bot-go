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
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/signaling"
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

	// prewarmedPeers menandai LID target yang sesinya sudah "dipanaskan" pada sesi
	// bot ini, agar prewarm hanya berjalan SEKALI per target (panggilan berikutnya
	// tetap cepat). Lihat PrewarmCallTarget.
	prewarmedPeers sync.Map // peerLID string → true
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

// AudioProvider menyiapkan AudioSource secara asinkron (mis. unduh + decode).
// Dipanggil SEKALI di background segera setelah panggilan dimulai, sehingga
// seluruh kerja berat (unduh lagu, decode, olah) menumpang waktu BERDERING dan
// tak menambah latensi inisiasi. Hasilnya diputar begitu lawan mengangkat.
type AudioProvider func(ctx context.Context) (meowcaller.AudioSource, error)

// StartCallProvider menelepon target dan memutar audio yang disiapkan oleh
// provide. Inti percepatan ada di sini: CallClient.Call (dering) dipicu LEBIH DULU,
// sementara provide() berjalan paralel di background. Saat OnReady (lawan angkat),
// barulah hasil provide ditunggu — yang praktis sudah selesai selama berdering.
func StartCallProvider(ctx context.Context, target string, provide AudioProvider, notify func(string)) (string, string, error) {
	if CallClient == nil {
		return "", "", fmt.Errorf("subsistem panggilan belum diinisialisasi")
	}

	// Mulai menyiapkan audio di BACKGROUND sekarang juga (paralel dengan dering).
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

	// Prewarm sesi/daftar-device ke target SEBELUM mengirim offer. Optimasi
	// "ring-first" memanggil terlalu cepat sehingga, pada panggilan PERTAMA ke
	// sebuah target, sesi Signal/relay ke peer belum matang → offer terbang lebih
	// dulu dan panggilan tampil "tak terjawab" seketika di HP lawan (pola lama yang
	// mengunduh dulu tak pernah kena karena unduhan memberi jeda alami). Prewarm
	// meniru jeda itu secara sengaja & SEKALI per target (lihat prewarmedPeers),
	// jadi panggilan berikutnya tetap cepat. Best-effort: kegagalan tak fatal.
	// Penyiapan audio (provide) sudah jalan paralel di atas, jadi waktu prewarm
	// tidak terbuang.
	PrewarmCallTarget(ctx, target)

	call, err := CallClient.Call(ctx, target)
	if err != nil {
		return "", "", err
	}

	callID := call.ID()
	peer := call.Peer().String()

	callMu.Lock()
	activeCalls[callID] = call
	callMu.Unlock()

	// Kirim mute_v2 sisi CALLER sekali, saat fase 'connecting'.
	//
	// PENTING: logika "callee menunda <accept> sampai mute_v2 caller tiba"
	// (engine.go onCallRaw) HANYA berlaku bila lawan juga memakai meowcaller
	// (bot↔bot). Saat menelepon WhatsApp MANUSIA, app WA mereka tak pakai logika
	// itu. Untuk OUTBOUND, fase 'connecting' dipicu oleh ACK RELAY (bukan oleh
	// lawan mengangkat), jadi mute_v2 terkirim ~milidetik setelah offer — JAUH
	// sebelum HP lawan menampilkan panggilan. Mengirim sinyal mid-call sedini itu
	// membuat sebagian app lawan menandai panggilan sebagai "tak terjawab" seketika.
	// Karena itu DEFAULT-nya MATI; aktifkan hanya untuk skenario bot↔bot dengan
	// env MEOW_CALLER_MUTE=1. Best-effort: kegagalan TIDAK mematikan panggilan.
	var muteOnce sync.Once
	call.OnStateChange(func(p meowcaller.CallPhase) {
		if p == meowcaller.CallPhaseConnecting && os.Getenv("MEOW_CALLER_MUTE") == "1" {
			muteOnce.Do(func() { sendCallerMute(callID, call.Peer()) })
		}
		if notify != nil {
			notify(phaseLabel(p))
		}
	})

	call.OnReady(func() {
		if provide == nil {
			return
		}
		// Tunggu hasil penyiapan background (praktis sudah selesai saat lawan mengangkat).
		<-prepareDone
		if prepareErr != nil {
			// Audio gagal disiapkan (mis. unduh lagu gagal). Tutup rapi; OnEnd akan
			// melaporkan "berakhir" ke chat (tak perlu notify ganda di sini).
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

// StartCallMP3Download menelepon target lalu, SELAMA BERDERING, memanggil download
// untuk mengambil byte MP3 dan menyiapkannya menjadi audio panggilan. Command cukup
// menyuplai fungsi unduh — seluruh decode/olah + sinkronisasi ke OnReady diurus di
// sini, dan package commands tak perlu mengenal meowcaller.
func StartCallMP3Download(ctx context.Context, target string, download func(context.Context) ([]byte, error), notify func(string)) (string, string, error) {
	provide := func(c context.Context) (meowcaller.AudioSource, error) {
		data, err := download(c)
		if err != nil {
			return nil, err
		}
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

// sendCallerMute mengirim <call><mute_v2 mute-state="false"></call> dari sisi
// CALLER (bot). Handshake panggilan WA mengharapkan caller mengirim mute_v2;
// sisi callee menunda <accept>-nya sampai mute_v2 caller tiba. Dipanggil sekali
// saat fase 'connecting' (relay+key sudah ada). Best-effort & non-fatal: kegagalan
// hanya dicatat, tidak menutup panggilan.
func sendCallerMute(callID string, peer types.JID) {
	if callWA == nil || callID == "" || peer.IsEmpty() {
		return
	}
	self := callWA.Store.GetLID()
	if self.IsEmpty() {
		return
	}
	node := signaling.BuildMuteV2(callID, peer, self, "false")
	node.Attrs["id"] = callWA.GenerateMessageID()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := callWA.DangerousInternals().SendNode(ctx, node); err != nil {
		Print("[CALL] ⚠️ Gagal kirim mute_v2 caller (best-effort): %v", err)
		return
	}
	Print("[CALL] 🔇 mute_v2 caller terkirim (call %s).", callID)
}

// resolveCallPeerLID menurunkan target (nomor, JID telepon, atau @lid) menjadi LID
// peer — alamat yang dipakai meowcaller untuk kunci E2E. Mengembalikan (LID, true)
// bila berhasil. Meniru resolvePeerLID milik meowcaller agar prewarm memanaskan
// IDENTITAS yang sama persis dengan yang dipakai saat offer (kalau beda, prewarm
// sia-sia). Best-effort.
func resolveCallPeerLID(ctx context.Context, target string) (types.JID, bool) {
	target = strings.TrimSpace(target)
	if target == "" || callWA == nil {
		return types.EmptyJID, false
	}
	var jid types.JID
	var err error
	if strings.ContainsRune(target, '@') {
		if jid, err = types.ParseJID(target); err != nil {
			return types.EmptyJID, false
		}
	} else {
		jid = types.NewJID(strings.TrimPrefix(target, "+"), types.DefaultUserServer)
	}
	if jid.Server == types.HiddenUserServer {
		return jid, true // sudah LID
	}
	if lid, err := callWA.Store.LIDs.GetLIDForPN(ctx, jid); err == nil && !lid.IsEmpty() {
		return lid, true
	}
	if info, err := callWA.GetUserInfo(ctx, []types.JID{jid}); err == nil {
		for _, ui := range info {
			if !ui.LID.IsEmpty() {
				return ui.LID, true
			}
		}
	}
	if lid, err := callWA.Store.LIDs.GetLIDForPN(ctx, jid); err == nil && !lid.IsEmpty() {
		return lid, true
	}
	return types.EmptyJID, false
}

// PrewarmCallTarget memanaskan jalur panggilan ke target SEBELUM offer dikirim:
// resolusi LID (warm cache), ambil daftar device (usync), lalu prefetch prekey
// bundle tiap device. Tujuannya menutup celah "panggilan pertama tak terjawab":
// pada call pertama ke sebuah target, jalur kripto/usync ke peer belum siap dan
// balapan dengan UI panggilan lawan → tampil missed. Prewarm menyiapkannya lebih
// dulu, hanya SEKALI per target per sesi (prewarmedPeers), sehingga panggilan
// berikutnya tetap cepat.
//
// AMAN: hanya MENGAMBIL bundle (FetchPreKeys) — tidak meng-enkripsi/menggeser
// ratchet Signal. Meng-enkripsi-lalu-membuang pesan justru berbahaya: ia memajukan
// ratchet sisi kita tanpa peer menerima pesan inisiasi, sehingga offer pkmsg
// berikutnya gagal didekripsi peer. Jadi sengaja TIDAK dilakukan di sini.
func PrewarmCallTarget(ctx context.Context, target string) {
	if callWA == nil {
		return
	}
	peerLID, ok := resolveCallPeerLID(ctx, target)
	if !ok {
		return
	}
	if _, warm := prewarmedPeers.Load(peerLID.String()); warm {
		return // sudah dipanaskan pada sesi ini
	}

	devices, err := callWA.GetUserDevices(ctx, []types.JID{peerLID})
	if err != nil || len(devices) == 0 {
		return // jangan tandai warm; biar dicoba lagi di panggilan berikutnya
	}
	// Prefetch prekey bundle agar enkripsi callKey saat offer tak menunggu round-trip
	// server. Tidak membangun/menggeser sesi — aman dari desync.
	callWA.DangerousInternals().FetchPreKeysNoError(ctx, devices)

	prewarmedPeers.Store(peerLID.String(), true)
	Print("[CALL] 🔥 Prewarm panggilan ke %s (%d device) selesai.", peerLID.String(), len(devices))
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
