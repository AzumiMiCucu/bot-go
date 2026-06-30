package src

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// KIRIM PESAN GRUP DENGAN EXCLUDE RECIPIENTS (setara Baileys sPR / relayMessage{exclude})
// =================================================================
// KOREKSI asumsi lama ("exclude tak feasible di whatsmeow"): TERNYATA BISA tanpa fork.
//
// Mekanisme protokol: server WhatsApp mem-fan-out stanza <message> grup HANYA ke
// device yang tercantum pada node <participants> di dalam stanza. Konten grup
// dienkripsi sekali sebagai skmsg (sender-key), tapi ROUTING-nya mengikuti daftar
// participant. Maka device yang TIDAK ada di daftar itu tak akan dikirimi pesan —
// persis efek `exclude` / `excludeMe` pada Baileys.
//
// whatsmeow membangun node <participants> dari argumen `participants []types.JID`
// pada (*Client).sendGroup. Method itu unexported, NAMUN dibuka lewat
// Client.DangerousInternals().SendGroup(...). Jadi: ambil daftar member grup,
// buang yang ingin di-exclude, panggil SendGroup dengan sisa daftar.
//
// Kendala: argumen terakhir SendGroup bertipe `nodeExtraParams` yang UNEXPORTED —
// tak bisa dinamai dari paket lain, jadi pemanggilan WAJIB lewat reflection. Untuk
// grup ber-addressing-mode LID, field `addressingMode` di struct itu di-set lewat
// unsafe (reflection tak bisa men-set field unexported tanpa itu). Reimplementasi
// penuh sendGroup mustahil karena encoder sender-key (`pbSerializer`) juga
// unexported — jadi SendGroup-via-reflection adalah satu-satunya jalur sah.
// =================================================================

// excludeSendMu menserialkan pemanggilan SendGroup jalur-reflection kita. Jalur
// normal whatsmeow memakai messageSendLock internal (tak ter-ekspos); mutex ini
// minimal mencegah dua exclude-send kita sendiri berjalan paralel.
var excludeSendMu sync.Mutex

// SendGroupExcluding mengirim `message` ke grup `chat`, tetapi TIDAK mengirimkannya
// ke device milik JID pada `exclude` (dan ke device akun sendiri bila excludeMe).
// Mengembalikan participant-hash (phash) dari server.
//
// `exclude` boleh berisi JID dalam bentuk PN (...@s.whatsapp.net) maupun LID
// (...@lid); keduanya otomatis dipetakan ke bentuk yang dipakai grup.
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

	di := client.DangerousInternals()

	// 1) Ambil daftar member + addressing mode grup (dari cache; fetch bila perlu).
	//    gmc bertipe *groupMetaCache (unexported), tapi field Members & AddressingMode
	//    EXPORTED → boleh diakses langsung meski tipenya tak bisa dinamai.
	gmc, err := di.GetCachedGroupData(ctx, chat)
	if err != nil {
		return "", fmt.Errorf("gagal ambil data grup: %w", err)
	}
	members := gmc.Members
	addrMode := gmc.AddressingMode
	if len(members) == 0 {
		return "", fmt.Errorf("daftar member grup kosong")
	}

	// 2) Identitas pengirim sesuai addressing mode grup.
	ownID := client.Store.GetJID()
	if addrMode == types.AddressingModeLID {
		ownID = client.Store.GetLID()
	}

	// 3) Susun himpunan User yang di-exclude. Member grup seragam (semua PN atau
	//    semua LID), sedangkan JID exclude bisa datang dalam bentuk apa pun, maka
	//    tiap exclude dipetakan ke KEDUA ruang (PN & LID) agar pasti cocok.
	excluded := make(map[string]bool)
	addExclude := func(j types.JID) {
		j = j.ToNonAD()
		if j.User == "" {
			return
		}
		excluded[j.User] = true
		if lid, e := client.Store.LIDs.GetLIDForPN(ctx, j); e == nil && !lid.IsEmpty() {
			excluded[lid.ToNonAD().User] = true
		}
		if pn, e := client.Store.LIDs.GetPNForLID(ctx, j); e == nil && !pn.IsEmpty() {
			excluded[pn.ToNonAD().User] = true
		}
	}
	for _, e := range exclude {
		addExclude(e)
	}
	if excludeMe {
		addExclude(client.Store.GetJID())
		addExclude(client.Store.GetLID())
	}

	// 4) Saring member.
	participants := make([]types.JID, 0, len(members))
	for _, m := range members {
		if excluded[m.ToNonAD().User] {
			continue
		}
		participants = append(participants, m)
	}
	if len(participants) == 0 {
		return "", fmt.Errorf("semua member ter-exclude, tak ada penerima tersisa")
	}

	// 5) ROTASI sender-key bila ada member yang di-exclude.
	//    INI KUNCI agar target dapat placeholder "Menunggu pesan ini" — BUKAN teks.
	//    Sebabnya: sender-key WA me-ratchet MAJU. Tanpa rotasi, device yang
	//    di-exclude tetap punya state key dari pesan sebelumnya & bisa menurunkan
	//    iterasi skmsg baru → teks tetap terbaca. Dengan mengosongkan sender-key
	//    tersimpan, SendGroup membuat key BARU (keyID baru), SKDM-nya hanya
	//    dibagikan ke participant tersisa; target tak punya key baru → gagal
	//    dekripsi → placeholder. Retry-receipt-nya tak terjawab (kita tak
	//    addRecentMessage) sehingga placeholder menetap, persis perilaku sPR Baileys.
	if len(exclude) > 0 {
		rotateGroupSenderKey(ctx, client, chat)
	}

	// 6) Panggil SendGroup lewat reflection (param terakhir bertipe unexported).
	return callSendGroup(ctx, di, ownID, chat, participants, msgID, message, addrMode)
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

// rotateGroupSenderKey mengosongkan sender-key BOT untuk grup tsb di store, agar
// SendGroup berikutnya menghasilkan sender-key BARU (lihat groups.SessionBuilder.
// Create: key baru dibuat saat record kosong). whatsmeow memakai senderKeyName =
// (group=chat.String(), sender=ownLID.SignalAddress()) — kita kosongkan baris itu.
//
// Efek samping wajar: seluruh member (yang tak di-exclude) menerima SKDM key baru
// pada pesan ini juga, jadi mereka tetap bisa baca. Pesan normal berikutnya akan
// memakai ulang key baru ini (re-distribusi ke semua), tapi target tetap tak bisa
// membaca pesan tersembunyi karena ratchet maju (tak bisa mundur ke iterasi awal).
func rotateGroupSenderKey(ctx context.Context, client *whatsmeow.Client, chat types.JID) {
	ownLID := client.Store.GetLID()
	if ownLID.IsEmpty() || client.Store.SenderKeys == nil {
		return
	}
	senderAddr := ownLID.SignalAddress().String()
	// session=nil → kolom sender_key di-set NULL → GetSenderKey balik nil →
	// LoadSenderKey balik record kosong → Create generate key baru.
	_ = client.Store.SenderKeys.PutSenderKey(ctx, chat.String(), senderAddr, nil)
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
			// Field unexported → tembus pakai unsafe agar bisa di-set.
			reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).
				Elem().
				Set(reflect.ValueOf(addrMode))
		}
	}

	excludeSendMu.Lock()
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
	excludeSendMu.Unlock()

	phash, _ = out[0].Interface().(string)
	if ev := out[2].Interface(); ev != nil {
		if e, ok := ev.(error); ok {
			return phash, e
		}
	}
	return phash, nil
}
