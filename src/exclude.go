package src

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sync"
	"unsafe"

	groupRecord "go.mau.fi/libsignal/groups/state/record"
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
// Jadi supaya target dapat placeholder "Menunggu pesan ini" (BUKAN teks):
//   (a) ROTASI sender-key dulu → keyID BARU. Kunci lama yang sudah dipegang target
//       jadi tak berlaku (sender-key WA ratchet MAJU, tak bisa mundur).
//   (b) Bagikan SKDM key baru ke semua participant KECUALI target (buang target dari
//       daftar participant yang dipass ke SendGroup).
//   → target tetap menerima skmsg (fan-out server) tapi tak punya key baru →
//     gagal dekripsi → placeholder. Retry-receipt target tak terjawab (jalur ini
//     skip addRecentMessage) → placeholder MENETAP. Persis perilaku sPR Baileys.
//
// whatsmeow tak mengekspos sendGroup, tapi membukanya lewat
// Client.DangerousInternals().SendGroup(participants, ...). Argumen terakhirnya
// bertipe unexported (nodeExtraParams) → pemanggilan WAJIB via reflection;
// field addressingMode di-set lewat unsafe utk grup mode LID.
// =================================================================

// ExcludeDebug: bila true, cetak diagnosa ke console (log standar) saat mengirim.
var ExcludeDebug = true

// ExcludePairwise memilih STRATEGI exclude:
//
//	true  → STRATEGY B (pairwise, TANPA skmsg). Isi pesan asli dienkripsi
//	        PAIRWISE (per-device, <enc type=msg/pkmsg>) HANYA ke device yang
//	        diizinkan, dibungkus dalam <participants> TANPA node <enc type=skmsg>
//	        tingkat-atas. Server hanya me-rute enc ke device yang tercantum →
//	        target ter-exclude TIDAK menerima apa pun → TAK ADA placeholder
//	        "Menunggu pesan ini". Inilah yang dipakai teman (Baileys).
//	false → STRATEGY A (skmsg lama). Server tetap fan-out skmsg ke semua device,
//	        target dapat placeholder karena sender-key dirotasi. Lihat
//	        rotateGroupSenderKey.
//
// Default Strategy B karena itu yang diminta (bersih, tanpa placeholder).
var ExcludePairwise = true

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

	// 5) STRATEGY B (default): pairwise, TANPA skmsg → target tak dapat apa pun,
	//    TIDAK ada placeholder. Ini yang diminta.
	if ExcludePairwise {
		phash, err := sendGroupPairwise(ctx, client, chat, message, msgID, participants, addrMode)
		if ExcludeDebug {
			log.Printf("[sembunyi] StrategyB(pairwise) selesai phash=%q err=%v (tanpa skmsg → target tak terima apa pun)", phash, err)
		}
		return phash, err
	}

	// 5b) STRATEGY A (lama): rotasi sender-key + skmsg → target dapat placeholder.
	if realExcluded > 0 {
		rotated := rotateGroupSenderKey(ctx, client, chat)
		if ExcludeDebug {
			log.Printf("[sembunyi] rotasi sender-key: %v (key diganti baru → target pegang key lama → gagal dekripsi)", rotated)
		}
	}

	// 6) Panggil SendGroup lewat reflection (param terakhir bertipe unexported).
	phash, err := callSendGroup(ctx, client.DangerousInternals(), ownID, chat, participants, msgID, message, addrMode)
	if ExcludeDebug {
		log.Printf("[sembunyi] SendGroup selesai phash=%q err=%v", phash, err)
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
	node, allDevices, err := callPrepareMessageNode(
		ctx, client.DangerousInternals(), chat, msgID, message, participants, addrMode,
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
	addrMode types.AddressingMode,
) (node *waBinary.Node, allDevices []types.JID, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panggilan PrepareMessageNode gagal (API whatsmeow mungkin berubah): %v", r)
		}
	}()

	plaintext, merr := proto.Marshal(message)
	if merr != nil {
		return nil, nil, fmt.Errorf("gagal marshal message: %w", merr)
	}

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
