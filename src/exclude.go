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

// ExcludePairwise memilih STRATEGI exclude:
//
//	false → STRATEGY C (skmsg + ROTASI sender-key + decrypt-fail=hide) = DEFAULT &
//	        BENAR. Inilah metode teman (Baileys/sPR): "ganti kunci ke kunci baru,
//	        bagikan kunci baru itu HANYA ke orang pilihan; yang tak punya kunci tak
//	        bisa lihat pesan". Konten dienkripsi sebagai skmsg dgn sender-key yang
//	        DIROTASI (keyID baru); SKDM kunci baru hanya dibagikan ke device yang
//	        diizinkan; node skmsg ditandai decrypt-fail="hide". Server tetap fan-out
//	        skmsg ke semua device grup, tapi target ter-exclude tak punya kunci baru →
//	        gagal dekripsi → karena decrypt-fail=hide, WA sembunyikan total → target
//	        lihat KOSONG (tanpa placeholder). Lihat sendGroupSkmsgHide.
//	true  → STRATEGY B (pairwise, TANPA skmsg) = SALAH/NON-FUNGSIONAL, disimpan hanya
//	        sbg catatan. WhatsApp resmi TIDAK me-render konten yang ditaruh di node
//	        <participants> tanpa skmsg (enc di situ dianggap material kunci/SKDM saja),
//	        jadi TAK ADA yang melihat pesan — termasuk penerima yang diizinkan. JANGAN
//	        dipakai. (Diverifikasi: relayMessage Baileys pun SELALU pakai skmsg utk grup.)
//
// CATATAN: kunci "target lihat KOSONG (bukan placeholder)" ADA di atribut
// decrypt-fail="hide" pada enc skmsg — BUKAN pada pairwise. Percobaan lama yang
// menyimpulkan "placeholder tak bisa dihilangkan selama skmsg dipakai" itu KELIRU:
// belum tahu soal decrypt-fail=hide.
var ExcludePairwise = false

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

	// 5) STRATEGY B (pairwise) — NON-FUNGSIONAL, hanya bila ExcludePairwise sengaja
	//    di-set true. WhatsApp resmi tak me-render konten di <participants> tanpa
	//    skmsg → tak ada yang lihat. JANGAN dipakai. (lihat doc ExcludePairwise)
	if ExcludePairwise {
		phash, err := sendGroupPairwise(ctx, client, chat, message, msgID, participants, addrMode)
		if ExcludeDebug {
			log.Printf("[sembunyi] StrategyB(pairwise) selesai phash=%q err=%v (NON-FUNGSIONAL)", phash, err)
		}
		return phash, err
	}

	// 5b) STRATEGY C (DEFAULT & BENAR = metode teman): rotasi sender-key ke kunci BARU,
	//     lalu kirim skmsg yang SKDM-nya hanya dibagikan ke `participants` (yang
	//     diizinkan) DAN node skmsg-nya ditandai decrypt-fail=hide. Target ter-exclude
	//     tak dapat kunci baru → gagal dekripsi → disembunyikan → KOSONG (tanpa placeholder).
	if realExcluded > 0 {
		rotated := rotateGroupSenderKey(ctx, client, chat)
		if ExcludeDebug {
			log.Printf("[sembunyi] rotasi sender-key: %v (key diganti baru → target pegang key lama → gagal dekripsi)", rotated)
		}
	}

	// 6) Bangun & kirim skmsg sendiri. decrypt-fail=hide diaktifkan HANYA saat ada
	//    target ter-exclude (kalau tak ada, kirim normal biar aman).
	phash, err := sendGroupSkmsgHide(ctx, client, ownID, chat, message, msgID, participants, addrMode, realExcluded > 0)
	if ExcludeDebug {
		log.Printf("[sembunyi] StrategyC(skmsg+hide) selesai phash=%q hide=%v err=%v", phash, realExcluded > 0, err)
	}
	return phash, err
}

// sendGroupPairwise = STRATEGY B. Membangun SATU stanza <message> berisi SATU node
// <participants> dengan enc PAIRWISE per-device (isi Message LENGKAP, bukan SKDM),
// TANPA <enc type=skmsg> tingkat-atas dan TANPA atribut phash — persis relayMessage
// Baileys jalur grup (sumber sPR.js teman):
//
//	<message to=GRUP id=... type=text addressing_mode=lid>
//	  <participants>
//	    <to jid=DEVICE><enc v=2 type=msg|pkmsg> …pairwise(Message penuh)… </enc></to>
//	    …hanya device yang diizinkan…
//	  </participants>
//	  [<device-identity> bila ada pkmsg]
//	</message>
//
// Tanpa skmsg, server WA tak mem-fan-out sender-key ke seluruh grup; ia hanya
// mengantar tiap enc ke device yang tercantum di <participants>. Device target yang
// di-exclude tak masuk daftar → tak menerima apa pun → TAK ADA placeholder.
//
// CATATAN PENTING (dua kegagalan sebelumnya):
//   - JANGAN set attrs["phash"]. whatsmeow.sendGroup men-set phash KARENA ada skmsg
//     (server memverifikasi daftar device untuk SKDM). Baileys jalur grup TIDAK
//     men-set phash. phash atas SUBSET device → server anggap daftar tak lengkap →
//     drop pesan (gejala: "sukses" tapi tak ada yang menerima). Inilah bug B-v1.
//   - JANGAN kirim N stanza terpisah ber-atribut `participant` (itu format
//     retry-response, hanya sah sebagai balasan receipt) — bug B-v2.
//
// prepareMessageNode whatsmeow membangun struktur <participants> ini persis (dipakai
// jalur DM & pembagian SKDM). Kita panggil versi terekspos lalu KIRIM apa adanya
// TANPA menambah skmsg maupun phash — itulah bedanya dengan sendGroup.
func sendGroupPairwise(
	ctx context.Context,
	client *whatsmeow.Client,
	chat types.JID,
	message *waProto.Message,
	msgID string,
	participants []types.JID,
	addrMode types.AddressingMode,
) (string, error) {
	fullPlaintext, merr := proto.Marshal(message)
	if merr != nil {
		return "", fmt.Errorf("gagal marshal message: %w", merr)
	}
	node, allDevices, err := callPrepareMessageNode(
		ctx, client.DangerousInternals(), chat, msgID, message, participants, fullPlaintext, addrMode,
	)
	if err != nil {
		return "", err
	}
	if node == nil {
		return "", fmt.Errorf("prepareMessageNode mengembalikan node kosong")
	}

	// KUNCI: JANGAN set phash. (Baileys jalur grup tidak men-set phash; menambahkannya
	// atas subset device membuat server men-drop pesan.)
	delete(node.Attrs, "phash")

	if ExcludeDebug {
		log.Printf("[sembunyi] StrategyB device_penerima=%d id=%s (participants pairwise, tanpa skmsg & tanpa phash)",
			len(allDevices), msgID)
	}

	// KIRIM apa adanya — TANPA menambahkan <enc type=skmsg> maupun phash.
	if serr := client.DangerousInternals().SendNode(ctx, *node); serr != nil {
		return "", fmt.Errorf("gagal kirim node pairwise: %w", serr)
	}
	return "", nil
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

// callSendGroup memanggil (*DangerousInternalClient).SendGroup via reflection dan
// men-set field unexported `addressingMode` lewat unsafe untuk grup mode LID.
//
// Tanda tangan target:
//
//	SendGroup(ctx, ownID, to types.JID, participants []types.JID,
//	          id types.MessageID, message *waE2E.Message,
//	          timings *MessageDebugTimings, extraParams nodeExtraParams)
//	          (string, []byte, error)
func callSendGroup(
	ctx context.Context,
	di *whatsmeow.DangerousInternalClient,
	ownID, to types.JID,
	participants []types.JID,
	msgID string,
	message *waProto.Message,
	addrMode types.AddressingMode,
) (phash string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panggilan SendGroup gagal (API whatsmeow mungkin berubah): %v", r)
		}
	}()

	method := reflect.ValueOf(di).MethodByName("SendGroup")
	if !method.IsValid() {
		return "", fmt.Errorf("DangerousInternals.SendGroup tidak ditemukan (versi whatsmeow tak kompatibel)")
	}
	mt := method.Type()
	if mt.NumIn() != 8 {
		return "", fmt.Errorf("tanda tangan SendGroup berubah (arg=%d, diharapkan 8)", mt.NumIn())
	}

	// Bangun nilai zero untuk nodeExtraParams (param terakhir) lalu set addressingMode.
	extra := reflect.New(mt.In(7)).Elem()
	if addrMode != "" {
		if f := extra.FieldByName("addressingMode"); f.IsValid() {
			reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).
				Elem().
				Set(reflect.ValueOf(addrMode))
		}
	}

	out := method.Call([]reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(ownID),
		reflect.ValueOf(to),
		reflect.ValueOf(participants),
		reflect.ValueOf(msgID),
		reflect.ValueOf(message),
		reflect.ValueOf(&whatsmeow.MessageDebugTimings{}),
		extra,
	})

	phash, _ = out[0].Interface().(string)
	if ev := out[2].Interface(); ev != nil {
		if e, ok := ev.(error); ok {
			return phash, e
		}
	}
	return phash, nil
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

	phash = participantListHashLocal(allDevices)
	node.Attrs["phash"] = phash

	skEnc := waBinary.Node{
		Tag:     "enc",
		Content: ciphertext,
		Attrs:   waBinary.Attrs{"v": "2", "type": "skmsg"},
	}
	if hideOnFail {
		// INI kuncinya: target yang gagal dekripsi disembunyikan (bukan placeholder).
		skEnc.Attrs["decrypt-fail"] = "hide"
	}
	node.Content = append(node.GetChildren(), skEnc)

	if ExcludeDebug {
		log.Printf("[sembunyi] StrategyC device_penerima=%d phash=%s hide=%v id=%s", len(allDevices), phash, hideOnFail, msgID)
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
