package src

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"go.mau.fi/libsignal/groups"
	groupRecord "go.mau.fi/libsignal/groups/state/record"
	signalProtocol "go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/util/keyhelper"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// KIRIM PESAN GRUP DENGAN EXCLUDE RECIPIENTS (setara Baileys sPR / relayMessage{exclude})
// =================================================================
// MEKANISME SEBENARNYA (diverifikasi dari sumber Baileys messages-send.ts):
//
//  1. Konten grup dienkripsi SEKALI sebagai skmsg (sender-key). Node <enc type=skmsg>
//     ini SATU di tingkat-atas stanza. Server WA mem-fan-out skmsg ke SEMUA device
//     anggota grup — TIDAK peduli isi node <participants>.
//  2. Node <participants> HANYA membawa SenderKeyDistributionMessage (SKDM) =
//     kunci grup, per-device. Device yang TIDAK menerima SKDM tak bisa dekripsi.
//
// Supaya target melihat KOSONG (bukan teks, bukan pula placeholder "Menunggu
// pesan ini") = STRATEGY C (BENAR, = metode teman/sPR Baileys):
//   (a) ROTASI sender-key dulu → keyID BARU. Kunci lama yang sudah dipegang target
//       jadi tak berlaku (sender-key WA ratchet MAJU, tak bisa mundur).
//   (b) Bagikan SKDM key baru ke semua participant KECUALI target (buang target dari
//       daftar participant yang dibangun ke <participants>).
//   (c) Tandai node <enc type=skmsg> dengan atribut decrypt-fail="hide".
//   → target tetap menerima skmsg (fan-out server) tapi tak punya key baru → gagal
//     dekripsi. KARENA decrypt-fail=hide, WhatsApp resmi TIDAK menampilkan placeholder
//     → pesan KOSONG total di sisi target. (decrypt-fail="" alias "show" = default =
//     placeholder "Menunggu pesan ini"; whatsmeow sendiri pakai "hide" utk
//     edit/reaction/poll — lihat send.go prepareMessageNode.)
//
// PENTING soal reimplement: whatsmeow tak mengekspos sendGroup, TAPI encoder
// sender-key `pbSerializer` = store.SignalProtobufSerializer yang EXPORTED
// (message.go: `var pbSerializer = store.SignalProtobufSerializer`). Jadi kita bisa
// membangun skmsg sendiri via libsignal `groups` + store.SignalProtobufSerializer,
// lalu meng-append <enc type=skmsg decrypt-fail=hide> secara manual. Node
// <participants> (SKDM per-device utk yang diizinkan) tetap dibangun lewat
// prepareMessageNode via reflection; field addressingMode di-set lewat unsafe utk
// grup mode LID. (Klaim lama "reimplement sendGroup mustahil" itu KELIRU.)
// =================================================================

// ExcludeDebug: bila true, cetak diagnosa ke console (log standar) saat mengirim.
var ExcludeDebug = true

// Metode exclude yang dipakai = STRATEGY C (skmsg + ROTASI sender-key +
// decrypt-fail=hide). Inilah metode teman (Baileys/sPR): "ganti kunci ke kunci baru,
// bagikan kunci baru itu HANYA ke orang pilihan; yang tak punya kunci tak bisa lihat
// pesan". Konten dienkripsi sebagai skmsg dgn sender-key yang DIROTASI (keyID baru);
// SKDM kunci baru hanya dibagikan ke device yang diizinkan; node skmsg ditandai
// decrypt-fail="hide". Server tetap fan-out skmsg ke semua device grup, tapi target
// ter-exclude tak punya kunci baru → gagal dekripsi → karena decrypt-fail=hide, WA
// sembunyikan total → target lihat KOSONG (tanpa placeholder). Lihat sendGroupSkmsgHide.
//
// (Strategi pairwise TANPA skmsg pernah dicoba & TERBUKTI non-fungsional — WhatsApp
// resmi tak me-render konten di node <participants> tanpa skmsg — jadi sudah dibuang.)

// ExcludeNonPrimaryDevices (permintaan user, "mirip sPR untuk device sekunder"):
// bila true, tiap pesan siluman (sembunyi/hidetagp lewat sendGroupSkmsgHide) HANYA
// membagikan sender-key (SKDM) ke device PRIMARY (device-index 0 = HP utama) tiap
// anggota. Device companion/non-primary (WA Web, Desktop, akun tertaut, bot) tak
// dapat kunci → gagal dekripsi → decrypt-fail=hide → KOSONG di sana. Jadi pesan bot
// tak pernah tampil di perangkat sekunder siapa pun.
//
// Syarat kebenaran: sender-key WAJIB dirotasi lebih dulu (device non-primary yang
// sudah pegang kunci lama tak boleh bisa derive pesan baru) — dijamin caller.
//
// DINONAKTIFKAN (false): saat true, `needHide` jadi SELALU aktif sehingga SETIAP
// pesan premium/sembunyi memangkas seluruh device non-primary + rotasi + hide —
// akibatnya penerima sah (mis. yang baca di WA Web/Desktop, atau saat SKDM baru
// tak sempat diterapkan di HP) melihat KOSONG. Itu membuat premium & sembunyi
// seolah "tak mengirim apa-apa". Dengan false, hide hanya jalan saat MEMANG ada
// target yang di-exclude (perilaku yang sudah terbukti bekerja).
var ExcludeNonPrimaryDevices = false

// excludeSendMu menserialkan rotasi+SendGroup jalur-reflection kita (messageSendLock
// internal whatsmeow tak ter-ekspos).
var excludeSendMu sync.Mutex

// SendGroupExcluding mengirim `message` ke grup `chat`, tetapi target pada `exclude`
// (dan device akun sendiri bila excludeMe) TIDAK akan bisa membacanya (placeholder).
// `exclude` boleh PN (...@s.whatsapp.net) maupun LID (...@lid) — keduanya dicocokkan
// ke anggota grup lewat data GroupInfo (tak bergantung cache LID).
func SendGroupExcluding(
	ctx context.Context,
	client *whatsmeow.Client,
	chat types.JID,
	message *waProto.Message,
	msgID string,
	exclude []types.JID,
	excludeMe bool,
	extraNodes ...waBinary.Node, // node tambahan (mis. biz native_flow utk tombol) diteruskan ke stanza
) (string, error) {
	return sendGroupExcludingImpl(ctx, client, chat, message, msgID, exclude, excludeMe, false, extraNodes...)
}

// SendGroupExcludingDev sama seperti SendGroupExcluding, TAPI selain meng-exclude
// target di `exclude`, juga membuang SEMUA device NON-PRIMARY (device-index != 0 =
// WA Web/Desktop/tablet/akun tertaut/bot) dari SETIAP anggota. Hanya HP UTAMA (device
// 0) tiap orang yang bisa membaca; perangkat sekunder siapa pun melihat KOSONG.
//
// Ini per-panggilan (opt-in eksplisit dari command `sembunyi -dev`), BUKAN flag global
// — jadi pengiriman normal (premium/sembunyi biasa) tak terpengaruh. Lihat catatan
// regresi pada ExcludeNonPrimaryDevices.
func SendGroupExcludingDev(
	ctx context.Context,
	client *whatsmeow.Client,
	chat types.JID,
	message *waProto.Message,
	msgID string,
	exclude []types.JID,
	excludeMe bool,
	extraNodes ...waBinary.Node,
) (string, error) {
	return sendGroupExcludingImpl(ctx, client, chat, message, msgID, exclude, excludeMe, true, extraNodes...)
}

// sendGroupExcludingImpl = inti bersama SendGroupExcluding / SendGroupExcludingDev.
// pruneNonPrimary=true → hanya device-0 tiap anggota yang menerima sender-key.
func sendGroupExcludingImpl(
	ctx context.Context,
	client *whatsmeow.Client,
	chat types.JID,
	message *waProto.Message,
	msgID string,
	exclude []types.JID,
	excludeMe bool,
	pruneNonPrimary bool,
	extraNodes ...waBinary.Node,
) (string, error) {
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return "", fmt.Errorf("client belum siap (belum login)")
	}
	if chat.Server != types.GroupServer {
		return "", fmt.Errorf("exclude recipients hanya untuk grup (g.us)")
	}
	if message == nil {
		return "", fmt.Errorf("message kosong")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if msgID == "" {
		msgID = GenerateAndroidMessageID()
	}

	// 1) GroupInfo = sumber kebenaran. Tiap participant punya .JID (bentuk yang
	//    dipakai whatsmeow utk kirim), .LID, dan .PhoneNumber → cocokkan target
	//    dalam bentuk apa pun tanpa bergantung cache LID.
	gi, err := client.GetGroupInfo(ctx, chat)
	if err != nil {
		return "", fmt.Errorf("gagal ambil info grup: %w", err)
	}
	addrMode := gi.AddressingMode

	// 2) Himpunan User target (kumpulkan SEMUA bentuk: apa adanya + User mentah).
	want := make(map[string]bool)
	for _, e := range exclude {
		if u := e.ToNonAD().User; u != "" {
			want[u] = true
		}
	}
	ownPNUser := client.Store.GetJID().ToNonAD().User
	ownLIDUser := client.Store.GetLID().ToNonAD().User
	if excludeMe {
		if ownPNUser != "" {
			want[ownPNUser] = true
		}
		if ownLIDUser != "" {
			want[ownLIDUser] = true
		}
	}

	// 3) Bangun daftar participant = part.JID semua anggota KECUALI yang cocok target.
	//    Cocok bila User target == part.JID / part.LID / part.PhoneNumber.
	participants := make([]types.JID, 0, len(gi.Participants))
	var excludedUsers []string
	for _, p := range gi.Participants {
		hit := want[p.JID.ToNonAD().User] ||
			(!p.LID.IsEmpty() && want[p.LID.ToNonAD().User]) ||
			(!p.PhoneNumber.IsEmpty() && want[p.PhoneNumber.ToNonAD().User])
		if hit {
			excludedUsers = append(excludedUsers, p.JID.User)
			continue
		}
		participants = append(participants, p.JID)
	}

	if len(participants) == 0 {
		return "", fmt.Errorf("semua member ter-exclude, tak ada penerima tersisa")
	}
	realExcluded := len(gi.Participants) - len(participants)

	// 4) Identitas pengirim sesuai addressing mode.
	ownID := client.Store.GetJID()
	if addrMode == types.AddressingModeLID {
		ownID = client.Store.GetLID()
	}

	if ExcludeDebug {
		log.Printf("[sembunyi] grup=%s mode=%q total_member=%d terkirim=%d ter-exclude=%d (%v) target_diminta=%d excludeMe=%v",
			chat.User, addrMode, len(gi.Participants), len(participants), realExcluded, excludedUsers, len(exclude), excludeMe)
		if realExcluded == 0 && (len(exclude) > 0) {
			log.Printf("[sembunyi] ⚠️ TIDAK ADA target yang cocok dgn anggota grup — target tak akan ter-exclude! Cek bentuk JID target.")
		}
	}

	excludeSendMu.Lock()
	defer excludeSendMu.Unlock()

	// 5b) STRATEGY C (DEFAULT & BENAR = metode teman): rotasi sender-key ke kunci BARU,
	//     lalu kirim skmsg yang SKDM-nya hanya dibagikan ke `participants` (yang
	//     diizinkan) DAN node skmsg-nya ditandai decrypt-fail=hide. Target ter-exclude
	//     tak dapat kunci baru → gagal dekripsi → disembunyikan → KOSONG (tanpa placeholder).
	// Rotasi WAJIB bila ada target ter-exclude ATAU bila kita mengeksklusi device
	// non-primary (agar device sekunder pemegang key lama tak bisa derive pesan baru).
	needHide := realExcluded > 0 || pruneNonPrimary || ExcludeNonPrimaryDevices
	if needHide {
		rotated := rotateGroupSenderKey(ctx, client, chat)
		if ExcludeDebug {
			log.Printf("[sembunyi] rotasi sender-key: %v (key diganti baru → pemegang key lama → gagal dekripsi)", rotated)
		}
	}

	// 6) Bangun & kirim skmsg sendiri. decrypt-fail=hide diaktifkan saat ada target
	//    ter-exclude atau saat mengeksklusi device non-primary.
	phash, err := sendGroupSkmsgHide(ctx, client, ownID, chat, message, msgID, participants, addrMode, needHide, pruneNonPrimary, extraNodes...)
	if ExcludeDebug {
		log.Printf("[sembunyi] StrategyC(skmsg+hide) selesai phash=%q hide=%v err=%v", phash, realExcluded > 0, err)
	}
	return phash, err
}

// callPrepareMessageNode memanggil (*DangerousInternalClient).PrepareMessageNode via
// reflection. Param terakhir (nodeExtraParams) bertipe unexported → dibangun via
// reflect.New; field addressingMode di-set lewat unsafe untuk grup mode LID.
//
// Tanda tangan target:
//
//	PrepareMessageNode(ctx, to types.JID, id types.MessageID, message *waE2E.Message,
//	    participants []types.JID, plaintext, dsmPlaintext []byte,
//	    timings *MessageDebugTimings, extraParams nodeExtraParams)
//	    (*waBinary.Node, []types.JID, error)
//
// plaintext = proto.Marshal(message) MENTAH (padding di dalam encryptMessageForDevice).
// dsmPlaintext = nil (grup tak punya device-sent message). Karena plaintext = Message
// PENUH (bukan SKDM), tiap <enc> di <participants> berisi konten pesan sesungguhnya.
func callPrepareMessageNode(
	ctx context.Context,
	di *whatsmeow.DangerousInternalClient,
	to types.JID,
	msgID string,
	message *waProto.Message,
	participants []types.JID,
	plaintext []byte,
	addrMode types.AddressingMode,
) (node *waBinary.Node, allDevices []types.JID, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panggilan PrepareMessageNode gagal (API whatsmeow mungkin berubah): %v", r)
		}
	}()

	method := reflect.ValueOf(di).MethodByName("PrepareMessageNode")
	if !method.IsValid() {
		return nil, nil, fmt.Errorf("DangerousInternals.PrepareMessageNode tidak ditemukan (versi whatsmeow tak kompatibel)")
	}
	mt := method.Type()
	if mt.NumIn() != 9 {
		return nil, nil, fmt.Errorf("tanda tangan PrepareMessageNode berubah (arg=%d, diharapkan 9)", mt.NumIn())
	}

	// Bangun nilai zero untuk nodeExtraParams (param terakhir) lalu set addressingMode.
	extra := reflect.New(mt.In(8)).Elem()
	if addrMode != "" {
		if f := extra.FieldByName("addressingMode"); f.IsValid() {
			reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).
				Elem().
				Set(reflect.ValueOf(addrMode))
		}
	}

	out := method.Call([]reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(to),
		reflect.ValueOf(msgID),
		reflect.ValueOf(message),
		reflect.ValueOf(participants),
		reflect.ValueOf(plaintext),
		reflect.ValueOf([]byte(nil)),
		reflect.ValueOf(&whatsmeow.MessageDebugTimings{}),
		extra,
	})

	if ev := out[2].Interface(); ev != nil {
		if e, ok := ev.(error); ok {
			return nil, nil, e
		}
	}
	node, _ = out[0].Interface().(*waBinary.Node)
	allDevices, _ = out[1].Interface().([]types.JID)
	return node, allDevices, nil
}

// SendSecretText = pembungkus praktis untuk pesan teks.
func SendSecretText(
	ctx context.Context,
	client *whatsmeow.Client,
	chat types.JID,
	text string,
	exclude []types.JID,
	excludeMe bool,
) (string, error) {
	msg := &waProto.Message{Conversation: proto.String(text)}
	return SendGroupExcluding(ctx, client, chat, msg, "", exclude, excludeMe)
}

// SendSecretTextDev = seperti SendSecretText tapi juga membuang seluruh device
// NON-PRIMARY (hanya HP utama tiap anggota yang bisa baca). Dipakai `sembunyi -dev`.
func SendSecretTextDev(
	ctx context.Context,
	client *whatsmeow.Client,
	chat types.JID,
	text string,
	exclude []types.JID,
	excludeMe bool,
) (string, error) {
	msg := &waProto.Message{Conversation: proto.String(text)}
	return SendGroupExcludingDev(ctx, client, chat, msg, "", exclude, excludeMe)
}

// rotateGroupSenderKey MENGGANTI sender-key BOT utk grup tsb dengan key BARU
// (keyID baru, iterasi 0). whatsmeow memakai senderKeyName =
// (group=chat.String(), sender=ownLID.SignalAddress()) — kita timpa baris itu.
//
// CATATAN: kolom sender_key di DB ber-constraint NOT NULL, jadi TAK BISA di-set
// NULL (percobaan lama gagal). Solusinya: bangun SenderKey record SAH yang baru
// (persis seperti groups.SessionBuilder.Create) lalu simpan Serialize()-nya.
// SendGroup berikut memuat record ini (non-kosong) → memakai key baru ini →
// SKDM key baru dibagikan ke participant tersisa; target hanya pegang key LAMA
// (keyID beda) → gagal dekripsi → placeholder.
func rotateGroupSenderKey(ctx context.Context, client *whatsmeow.Client, chat types.JID) bool {
	if client.Store.SenderKeys == nil {
		return false
	}
	// whatsmeow SELALU pakai getOwnLID() utk senderKeyName grup (lihat sendGroup).
	sender := client.Store.GetLID()
	if sender.IsEmpty() {
		// Fallback: sebagian sesi lama mungkin tak punya LID. Coba PN juga.
		sender = client.Store.GetJID()
	}
	if sender.IsEmpty() {
		return false
	}
	groupID := chat.String()
	addr := sender.SignalAddress().String()

	signingKey, err := keyhelper.GenerateSenderSigningKey()
	if err != nil {
		if ExcludeDebug {
			log.Printf("[sembunyi] gagal generate signing key: %v", err)
		}
		return false
	}
	rec := groupRecord.NewSenderKey(
		store.SignalProtobufSerializer.SenderKeyRecord,
		store.SignalProtobufSerializer.SenderKeyState,
	)
	rec.SetSenderKeyState(
		keyhelper.GenerateSenderKeyID(), 0,
		keyhelper.GenerateSenderKey(),
		signingKey,
	)
	if err := client.Store.SenderKeys.PutSenderKey(ctx, groupID, addr, rec.Serialize()); err != nil {
		if ExcludeDebug {
			log.Printf("[sembunyi] gagal simpan sender-key baru: %v", err)
		}
		return false
	}
	return true
}

// sendGroupSkmsgHide = STRATEGY C (DEFAULT & BENAR). Reimplementasi sendGroup
// whatsmeow, TAPI meng-append <enc type=skmsg> dengan atribut decrypt-fail="hide".
//
// Alur (persis whatsmeow.sendGroup, semua bagian pakai API yang bisa diakses):
//  1. builder = groups.NewGroupSessionBuilder(Store, store.SignalProtobufSerializer)
//     — pbSerializer whatsmeow SAMA dengan store.SignalProtobufSerializer (EXPORTED).
//  2. senderKeyName = (group=chat, sender=ownLID.SignalAddress()) — whatsmeow SELALU
//     pakai getOwnLID utk grup. builder.Create → SKDM utk sender-key SAAT INI (yang
//     baru saja dirotasi oleh pemanggil bila ada target).
//  3. skmsg = groups.NewGroupCipher(...).Encrypt(padMessage(marshal(message))).
//  4. <participants> (SKDM per-device utk `participants` yang diizinkan) dibangun lewat
//     prepareMessageNode (reflection). SKDM = skdPlaintext, jadi tiap enc di
//     <participants> membawa KUNCI (bukan konten) — persis pola grup normal.
//  5. append <enc type=skmsg [decrypt-fail=hide]> + set phash = hash(allDevices).
//  6. SendNode.
//
// hideOnFail=true → set decrypt-fail=hide (target gagal dekripsi disembunyikan → KOSONG).
// hideOnFail=false → skmsg normal (dipakai saat tak ada target ter-exclude).
func sendGroupSkmsgHide(
	ctx context.Context,
	client *whatsmeow.Client,
	ownID, chat types.JID,
	message *waProto.Message,
	msgID string,
	participants []types.JID,
	addrMode types.AddressingMode,
	hideOnFail bool,
	pruneNonPrimary bool,
	extraNodes ...waBinary.Node,
) (phash string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sendGroupSkmsgHide gagal (API whatsmeow/libsignal mungkin berubah): %v", r)
		}
	}()

	plaintext, merr := proto.Marshal(message)
	if merr != nil {
		return "", fmt.Errorf("gagal marshal message: %w", merr)
	}

	// whatsmeow SELALU pakai getOwnLID() utk senderKeyName grup.
	sender := client.Store.GetLID()
	if sender.IsEmpty() {
		sender = client.Store.GetJID()
	}
	if sender.IsEmpty() {
		return "", fmt.Errorf("tak ada identitas pengirim (LID/JID kosong)")
	}

	builder := groups.NewGroupSessionBuilder(client.Store, store.SignalProtobufSerializer)
	senderKeyName := signalProtocol.NewSenderKeyName(chat.String(), sender.SignalAddress())

	// SKDM utk sender-key saat ini (sudah dirotasi oleh pemanggil bila realExcluded>0).
	signalSKDMessage, err := builder.Create(ctx, senderKeyName)
	if err != nil {
		return "", fmt.Errorf("gagal buat SKDM: %w", err)
	}
	skdMessage := &waProto.Message{
		SenderKeyDistributionMessage: &waProto.SenderKeyDistributionMessage{
			GroupID:                             proto.String(chat.String()),
			AxolotlSenderKeyDistributionMessage: signalSKDMessage.Serialize(),
		},
	}
	skdPlaintext, merr := proto.Marshal(skdMessage)
	if merr != nil {
		return "", fmt.Errorf("gagal marshal SKDM: %w", merr)
	}

	// Enkripsi konten sebagai skmsg (sender-key).
	cipher := groups.NewGroupCipher(builder, senderKeyName, client.Store)
	encrypted, err := cipher.Encrypt(ctx, padMessageLocal(plaintext))
	if err != nil {
		return "", fmt.Errorf("gagal enkripsi skmsg: %w", err)
	}
	ciphertext := encrypted.SignedSerialize()

	// <participants> membawa SKDM (bukan konten) HANYA ke device yang diizinkan.
	di := client.DangerousInternals()
	node, allDevices, err := callPrepareMessageNode(ctx, di, chat, msgID, message, participants, skdPlaintext, addrMode)
	if err != nil {
		return "", err
	}
	if node == nil {
		return "", fmt.Errorf("prepareMessageNode mengembalikan node kosong")
	}

	// 2A: buang device NON-PRIMARY (device-index != 0) dari distribusi SKDM. Companion
	// (WA Web/Desktop/akun tertaut/bot) tak dapat sender-key → gagal dekripsi → hide →
	// KOSONG di sana. phash dihitung ulang atas HANYA device-0 yang tersisa agar cocok
	// dgn <participants> yang benar-benar dikirim.
	if pruneNonPrimary || ExcludeNonPrimaryDevices {
		if kept, dropped := pruneNonPrimaryDeviceNodes(node); dropped > 0 && len(kept) > 0 {
			allDevices = kept
			hideOnFail = true // yang gagal dekripsi (device sekunder) WAJIB disembunyikan
			if ExcludeDebug {
				log.Printf("[sembunyi] non-primary di-exclude: %d device sekunder dibuang, %d device primary tersisa", dropped, len(kept))
			}
		}
	}

	phash = participantListHashLocal(allDevices)
	node.Attrs["phash"] = phash

	skEnc := waBinary.Node{
		Tag:     "enc",
		Content: ciphertext,
		Attrs:   waBinary.Attrs{"v": "2", "type": "skmsg"},
	}
	// WAJIB utk media (gambar/video/stiker/dll): tanpa mediatype, WA tak me-render
	// media walau dekripsi sukses. sendGroup asli whatsmeow selalu men-set ini.
	if mt := mediaTypeFromMessageLocal(message); mt != "" {
		skEnc.Attrs["mediatype"] = mt
	}
	if hideOnFail {
		// INI kuncinya: target yang gagal dekripsi disembunyikan (bukan placeholder).
		skEnc.Attrs["decrypt-fail"] = "hide"
	}
	node.Content = append(node.GetChildren(), skEnc)
	// Node tambahan (mis. <biz><interactive native_flow> utk tombol) = sibling di stanza
	// <message>, persis SendRequestExtra.AdditionalNodes pada jalur kirim normal.
	if len(extraNodes) > 0 {
		node.Content = append(node.GetChildren(), extraNodes...)
	}

	if ExcludeDebug {
		log.Printf("[sembunyi] StrategyC device_penerima=%d phash=%s hide=%v id=%s extra=%d", len(allDevices), phash, hideOnFail, msgID, len(extraNodes))
	}

	if serr := di.SendNode(ctx, *node); serr != nil {
		return phash, fmt.Errorf("gagal kirim node skmsg: %w", serr)
	}
	return phash, nil
}

// padMessageLocal mereplika whatsmeow.padMessage (pad 1..15 byte, tiap byte = jumlah pad).
func padMessageLocal(plaintext []byte) []byte {
	var b [1]byte
	_, _ = rand.Read(b[:])
	pad := b[0] & 0x0f
	if pad == 0 {
		pad = 0x0f
	}
	return append(plaintext, bytes.Repeat([]byte{pad}, int(pad))...)
}

// mediaTypeFromMessageLocal mereplika whatsmeow.getMediaTypeFromMessage — nilai atribut
// `mediatype` pada node enc. Kosong = pesan non-media (teks biasa).
func mediaTypeFromMessageLocal(msg *waProto.Message) string {
	switch {
	case msg.ViewOnceMessage != nil:
		return mediaTypeFromMessageLocal(msg.ViewOnceMessage.Message)
	case msg.ViewOnceMessageV2 != nil:
		return mediaTypeFromMessageLocal(msg.ViewOnceMessageV2.Message)
	case msg.ViewOnceMessageV2Extension != nil:
		return mediaTypeFromMessageLocal(msg.ViewOnceMessageV2Extension.Message)
	case msg.LottieStickerMessage != nil:
		return mediaTypeFromMessageLocal(msg.LottieStickerMessage.Message)
	case msg.EphemeralMessage != nil:
		return mediaTypeFromMessageLocal(msg.EphemeralMessage.Message)
	case msg.DocumentWithCaptionMessage != nil:
		return mediaTypeFromMessageLocal(msg.DocumentWithCaptionMessage.Message)
	case msg.ExtendedTextMessage != nil && msg.ExtendedTextMessage.Title != nil:
		return "url"
	case msg.ImageMessage != nil:
		return "image"
	case msg.StickerMessage != nil:
		return "sticker"
	case msg.DocumentMessage != nil:
		return "document"
	case msg.AudioMessage != nil:
		if msg.AudioMessage.GetPTT() {
			return "ptt"
		}
		return "audio"
	case msg.VideoMessage != nil:
		if msg.VideoMessage.GetGifPlayback() {
			return "gif"
		}
		return "video"
	case msg.ContactMessage != nil:
		return "vcard"
	case msg.ContactsArrayMessage != nil:
		return "contact_array"
	case msg.ListMessage != nil:
		return "list"
	case msg.ListResponseMessage != nil:
		return "list_response"
	case msg.ButtonsResponseMessage != nil:
		return "buttons_response"
	case msg.OrderMessage != nil:
		return "order"
	case msg.ProductMessage != nil:
		return "product"
	case msg.InteractiveResponseMessage != nil:
		return "native_flow_response"
	default:
		return ""
	}
}

// SendGroupPersonalMention = "hidetag personal". SETIAP anggota grup menerima SATU
// pesan yang HANYA bisa DIA baca, berisi mention dirinya sendiri (@dia). Anggota lain
// menerima skmsg yang sama (server WA selalu fan-out), tetapi tak punya sender-key
// baru untuk pesan itu → gagal dekripsi → decrypt-fail=hide → KOSONG bagi mereka.
//
// Efeknya: tiap orang seolah menerima satu pesan tag yang menyebut DIRINYA, padahal
// isi ciphertext-nya berbeda per orang. Ini persis mekanisme [[feature-exclude-recipients]]
// STRATEGY C, hanya dijalankan berulang — sekali per anggota — dengan participant
// tunggal (si target) tiap kalinya.
//
// build(target) mengembalikan *waProto.Message khusus utk target itu (biasanya
// ExtendedTextMessage dgn ContextInfo.MentionedJID = [target]). Bila build balik nil,
// target itu dilewati.
//
// excludeMe=true → bot sendiri tak jadi sasaran (tak menerima N pesan ke device-nya).
// onProgress (boleh nil) dipanggil tiap satu pesan sukses terkirim.
//
// Mengembalikan (jumlah_terkirim, total_sasaran, error).
func SendGroupPersonalMention(
	ctx context.Context,
	client *whatsmeow.Client,
	chat types.JID,
	build func(target types.JID) *waProto.Message,
	excludeMe bool,
	onProgress func(sent, total int),
) (sent int, total int, err error) {
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return 0, 0, fmt.Errorf("client belum siap (belum login)")
	}
	if chat.Server != types.GroupServer {
		return 0, 0, fmt.Errorf("personal mention hanya untuk grup (g.us)")
	}
	if build == nil {
		return 0, 0, fmt.Errorf("build message kosong")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	gi, err := client.GetGroupInfo(ctx, chat)
	if err != nil {
		return 0, 0, fmt.Errorf("gagal ambil info grup: %w", err)
	}
	addrMode := gi.AddressingMode

	// Identitas pengirim sesuai addressing mode (sama seperti SendGroupExcluding).
	ownID := client.Store.GetJID()
	if addrMode == types.AddressingModeLID {
		ownID = client.Store.GetLID()
	}
	ownPNUser := client.Store.GetJID().ToNonAD().User
	ownLIDUser := client.Store.GetLID().ToNonAD().User

	// Sasaran = anggota yang punya .JID (bentuk yang dipakai whatsmeow utk kirim &
	// pembagian SKDM). Anggota yang cuma punya LID kosong .JID → tak bisa jadi
	// participant tunggal skmsg, dilewati.
	targets := make([]types.JID, 0, len(gi.Participants))
	for _, p := range gi.Participants {
		if p.JID.User == "" {
			continue
		}
		if excludeMe {
			isMe := (ownPNUser != "" && p.JID.ToNonAD().User == ownPNUser) ||
				(!p.LID.IsEmpty() && ownLIDUser != "" && p.LID.ToNonAD().User == ownLIDUser)
			if isMe {
				continue
			}
		}
		targets = append(targets, p.JID)
	}
	total = len(targets)
	if total == 0 {
		return 0, 0, fmt.Errorf("tak ada anggota yang bisa ditarget")
	}

	// Serialkan seluruh operasi (rotasi sender-key + kirim) supaya tak balapan dgn
	// jalur kirim lain — messageSendLock internal whatsmeow tak terekspos.
	excludeSendMu.Lock()
	defer excludeSendMu.Unlock()

	// ID pesan HARUS UNIK per anggota. Memakai satu ID sama untuk N pesan berbeda ke
	// grup yang sama membuat WhatsApp men-dedup: hanya pesan PERTAMA yang ditampilkan,
	// sisanya di-drop sebagai duplikat → mayoritas anggota tak menerima apa-apa. Jadi
	// tiap iterasi memakai GenerateAndroidMessageID() sendiri.
	for _, t := range targets {
		msg := build(t)
		if msg == nil {
			continue
		}
		// Rotasi sender-key BOT ke kunci BARU (keyID baru) sebelum tiap pesan. SKDM
		// kunci baru itu HANYA dibagikan ke `t`, jadi anggota lain tak bisa mendekripsi
		// pesan ini → disembunyikan. Ratchet maju: pemegang key lama tak bisa derive
		// pesan berkey baru → tetap kosong.
		rotateGroupSenderKey(ctx, client, chat)

		if _, serr := sendGroupSkmsgHide(
			ctx, client, ownID, chat, msg, GenerateAndroidMessageID(),
			[]types.JID{t}, addrMode, true, false,
		); serr != nil {
			if ExcludeDebug {
				log.Printf("[hidetagp] gagal kirim ke %s: %v", t.User, serr)
			}
			continue
		}
		sent++
		if onProgress != nil {
			onProgress(sent, total)
		}
	}
	return sent, total, nil
}

// participantListHashLocal mereplika whatsmeow.participantListHashV2.
func participantListHashLocal(participants []types.JID) string {
	s := make([]string, len(participants))
	for i, p := range participants {
		s[i] = p.ADString()
	}
	sort.Strings(s)
	h := sha256.Sum256([]byte(strings.Join(s, "")))
	return "2:" + base64.RawStdEncoding.EncodeToString(h[:6])
}

// pruneNonPrimaryDeviceNodes membuang entri <to jid=DEVICE> pada node <participants>
// yang device-index-nya != 0 (bukan HP utama). Mengembalikan daftar device PRIMARY
// yang dipertahankan + jumlah device sekunder yang dibuang.
//
// Struktur (dari whatsmeow.prepareMessageNode):
//
//	<message ...>
//	  <participants>
//	    <to jid=DEVICE_JID><enc .../></to>   ← jid = types.JID dgn .Device
//	    ...
//	  </participants>
//	  [<device-identity/>]
//	</message>
//
// Node non-<to> di dalam <participants> (kalau ada) dipertahankan apa adanya.
func pruneNonPrimaryDeviceNodes(node *waBinary.Node) (kept []types.JID, dropped int) {
	children := node.GetChildren()
	for i := range children {
		if children[i].Tag != "participants" {
			continue
		}
		toNodes := children[i].GetChildren()
		keptNodes := make([]waBinary.Node, 0, len(toNodes))
		for _, tn := range toNodes {
			if tn.Tag != "to" {
				keptNodes = append(keptNodes, tn)
				continue
			}
			jid, ok := jidFromNodeAttr(tn.Attrs["jid"])
			if ok && jid.Device != 0 {
				dropped++ // device sekunder → tak dibagikan sender-key
				continue
			}
			if ok {
				kept = append(kept, jid)
			}
			keptNodes = append(keptNodes, tn)
		}
		children[i].Content = keptNodes
	}
	return kept, dropped
}

// jidFromNodeAttr membaca atribut jid pada node biner (bisa types.JID atau string).
func jidFromNodeAttr(v any) (types.JID, bool) {
	switch j := v.(type) {
	case types.JID:
		return j, true
	case string:
		if pj, err := types.ParseJID(j); err == nil {
			return pj, true
		}
	}
	return types.JID{}, false
}
