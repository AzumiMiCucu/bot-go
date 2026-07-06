package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// ANTI-SPR — DETEKSI PESAN TERSEMBUNYI (sPR) DI GRUP
//
// Lawan dari fitur `sembunyi`/sPR (lihat src/exclude.go). Saat seseorang di grup
// mengirim pesan dengan teknik sPR/relayMessage{exclude} yang MENYEMBUNYIKAN bot
// (bot dibuang dari distribusi sender-key), server WA tetap mem-fan-out node
// <enc type=skmsg> ke bot, TAPI bot tak punya sender-key untuk pesan itu →
// gagal dekripsi. whatsmeow lalu memancarkan events.UndecryptableMessage.
//
// TANDA TANGAN sPR — DUA JALUR (kedua-duanya kini ditangkap):
//
//   (1) decrypt-fail=hide  → pengirim menandai node <enc type=skmsg> dengan
//       atribut decrypt-fail="hide" supaya device yang gagal dekripsi menampilkan
//       KOSONG (bukan placeholder "Menunggu pesan ini"). Ini dilakukan oleh
//       fitur `sembunyi` bot-go SENDIRI *dan* oleh sPR Baileys teman user
//       (relayHiddenGroupMessage: attrs {type:"skmsg","decrypt-fail":"hide"}).
//       → evt.DecryptFailMode == events.DecryptFailHide. Sinyal KUAT → alert cepat.
//
//   (2) skmsg gagal-dekripsi TANPA hide (decrypt-fail="" / "show"), tapi pengirim
//       SENGAJA tak pernah kirim-ulang (getMessage=undefined / removeRecentMessage).
//       whatsmeow menandai ini IsUnavailable=true (skmsg + ErrNoSenderKeyForUser,
//       message.go:397). MASALAHNYA: desync sender-key NORMAL (anggota baru join,
//       ganti HP) juga IsUnavailable=true — BEDANYA pengirim jujur akan MENGIRIM
//       ULANG saat diminta retry → pesan muncul sebagai events.Message ber-ID sama
//       dalam beberapa detik. sPR TIDAK pernah resend. Maka jalur (2) DITUNDA:
//       jika ID tak muncul-kembali dalam antisprConfirmWindow → itu sPR → alert.
//
// Instrumentasi: tiap UndecryptableMessage di grup direkam ke ring-buffer
// (antispr log) dan, bila `antispr debug on`, di-echo ke grup — untuk reproduksi
// bareng teman & melihat field asli (IsUnavailable/UnavailableType/DecryptFailMode).
//
// Diaktifkan per-grup (default OFF). Owner/admin yang mengatur.
// =================================================================

const (
	// antisprDedupTTL: dedup alert per (grup, message-ID). Satu aksi sPR bisa memicu
	// beberapa event untuk ID SAMA (retry-receipt mengulang; undecryptable lalu
	// resend→events.Message). Dedup per-ID = tiap pesan sPR di-alert TEPAT sekali,
	// LANGSUNG — pesan sPR BARU (ID baru) tak tertahan cooldown pengirim (dulu 45s/
	// pengirim bikin sPR beruntun "kadang-kadang doang" tak teralert).
	antisprDedupTTL = 2 * time.Minute

	// antisprConfirmWindow: untuk jalur (2) tanpa hide, tunggu selama ini untuk
	// melihat apakah pesan di-resend (→ desync normal, batalkan). Bila lewat tanpa
	// resend → konfirmasi sPR. WA retry-receipt biasanya dijawab < beberapa detik.
	antisprConfirmWindow = 8 * time.Second

	// antisprLogMax: jumlah entri dump yang disimpan untuk `antispr log`.
	antisprLogMax = 40
)

var (
	antisprSeen   = make(map[string]time.Time) // key = grup|message-ID → waktu alert (dedup)
	antisprSeenMu sync.Mutex

	// antisprPending melacak kandidat jalur-(2) yang menunggu konfirmasi
	// tak-di-resend. key = grup|msgID.
	antisprPending   = make(map[string]bool)
	antisprPendingMu sync.Mutex

	// antisprDecrypted: ID pesan (grup|msgID) yang BERHASIL didekripsi (resend
	// datang) → membatalkan kandidat pending. Dibersihkan berkala.
	antisprDecrypted   = make(map[string]time.Time)
	antisprDecryptedMu sync.Mutex

	// antisprDebug: grup yang mengaktifkan echo diagnostik ke grup (in-memory).
	antisprDebug   = make(map[string]bool)
	antisprDebugMu sync.RWMutex

	// antisprRing: ring-buffer dump lintas-grup untuk `antispr log`.
	antisprRing   []antisprDumpEntry
	antisprRingMu sync.Mutex
)

type antisprDumpEntry struct {
	When            time.Time
	Group           string
	SenderUser      string
	SenderDevice    uint16
	MsgID           string
	IsUnavailable   bool
	UnavailableType string
	FailMode        string
	Verdict         string // "hide", "no-resend", "desync?", "echo"
}

var reAntiSPRArgs = regexp.MustCompile(`(?i)^\s*antispr(?:\s+(.+))?\s*$`)

func init() {
	RegisterCommand(Command{
		Name:        "Anti-SPR",
		Category:    "Group",
		Aliases:     []string{"antispr", "antibisik", "antisembunyi"},
		Pattern:     reAntiSPRArgs,
		Description: "Deteksi pesan tersembunyi (sPR) di grup → alert tag pengirim: on/off/status/log/debug/help (admin)",
		Execute:     ExecuteAntiSPRToggle,
	}).Use(GroupOnlyMiddleware)
}

// NoteMessageDecrypted dipanggil dari eventHandler untuk tiap events.Message di
// grup yang BERHASIL didekripsi. Menandai (grup|ID) sudah dekripsi → membatalkan
// kandidat sPR jalur-(2) yang sedang menunggu (resend = desync normal, bukan sPR).
func NoteMessageDecrypted(chat types.JID, id string) {
	if chat.Server != types.GroupServer || id == "" {
		return
	}
	key := chat.ToNonAD().String() + "|" + id
	antisprDecryptedMu.Lock()
	antisprDecrypted[key] = time.Now()
	// GC ringan: buang entri > 2 menit.
	if len(antisprDecrypted) > 512 {
		cut := time.Now().Add(-2 * time.Minute)
		for k, t := range antisprDecrypted {
			if t.Before(cut) {
				delete(antisprDecrypted, k)
			}
		}
	}
	antisprDecryptedMu.Unlock()
}

// OnGroupMessage dipanggil dari eventHandler untuk tiap events.Message di grup
// yang BERHASIL didekripsi. Dua tugas:
//  1. NoteMessageDecrypted → batalkan kandidat sPR jalur-(2) yang menunggu.
//  2. InspectHideAttr → DETEKSI sPR walau bot BUKAN target: node <enc skmsg> yang
//     bot berhasil dekripsi tetapi bertanda decrypt-fail=hide pada pesan teks/media
//     biasa = pengirim menyembunyikan pesan itu dari sebagian anggota lain.
func OnGroupMessage(client *whatsmeow.Client, evt *events.Message) {
	if evt == nil {
		return
	}
	NoteMessageDecrypted(evt.Info.Chat, evt.Info.ID)
	inspectHideAttr(client, evt)
}

// inspectHideAttr mendeteksi sPR dari sisi PENERIMA yang BISA membaca pesan (bot
// bukan target exclude). Sinyal: node <enc type=skmsg> ber-atribut decrypt-fail=hide
// pada pesan konten biasa. WA resmi hanya pakai hide utk reaction/poll-update/edit.
func inspectHideAttr(client *whatsmeow.Client, evt *events.Message) {
	if evt.Info.IsFromMe || !evt.Info.IsGroup {
		return
	}
	groupID := evt.Info.Chat.ToNonAD().String()
	// Hemat: hanya kerja bila anti-sPR / debug aktif di grup ini.
	on := src.DB.IsGroupAntiSPR(groupID)
	dbg := isGroupAntiSPRDebug(groupID)
	if !on && !dbg {
		return
	}
	raw, ok := src.LookupRawNode(evt.Info.ID)
	if !ok || !raw.Available {
		return // node mentah tak tertangkap → tak bisa menilai
	}
	// Sinyal sPR = ADA <enc type=skmsg decrypt-fail=hide> di node (scan SEMUA enc,
	// bukan cuma pertama — node punya msg/pkmsg SKDM DULU lalu skmsg konten).
	if !raw.EncHideAny || raw.HideEncType != "skmsg" {
		return // bukan pesan grup konten ber-hide
	}
	// Kecualikan tipe yang WA resmi memang boleh set hide (reaction/poll-update/edit).
	if !isNormalContentMessage(evt.Message) {
		return
	}

	sender := evt.Info.Sender
	recordAntiSPRDump(antisprDumpEntry{
		When:            time.Now(),
		Group:           evt.Info.Chat.User,
		SenderUser:      sender.User,
		SenderDevice:    sender.Device,
		MsgID:           evt.Info.ID,
		IsUnavailable:   false,
		UnavailableType: "",
		FailMode:        "hide",
		Verdict:         "hide-visible", // bot BISA baca, tapi hide utk anggota lain
	})
	fmt.Printf("[ANTISPR] pesan-terbaca ber-hide grup=%s pengirim=%s dev=%d id=%q → sPR (menyembunyikan anggota lain)\n",
		evt.Info.Chat.User, sender.User, sender.Device, evt.Info.ID)

	if dbg {
		text := fmt.Sprintf(
			"🐞 *anti-sPR DEBUG (terbaca+hide)*\n"+
				"pengirim: @%s (dev %d)\n"+
				"id: `%s`\n"+
				"EncType: skmsg | decrypt-fail: hide\n"+
				"→ bot BISA baca, tapi pesan disembunyikan dari sebagian anggota",
			sender.User, sender.Device, evt.Info.ID)
		msg := &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: &waProto.ContextInfo{MentionedJID: []string{sender.ToNonAD().String()}},
		}}
		client.SendMessage(context.Background(), evt.Info.Chat, msg, AndroidExtra())
	}

	if on {
		fireAntiSPRAlertJID(client, evt.Info.Chat, sender, evt.Info.ID, "hide-visible")
	}
}

// isNormalContentMessage: true bila pesan adalah konten "biasa" (teks/media/dll) —
// yakni BUKAN reaction/poll-update/protocol(edit/revoke) yang WA resmi memang boleh
// tandai decrypt-fail=hide. Membedakan sPR (hide pada konten) dari hide sah.
func isNormalContentMessage(m *waProto.Message) bool {
	if m == nil {
		return false
	}
	inner := m
	if e := inner.GetEphemeralMessage(); e.GetMessage() != nil {
		inner = e.GetMessage()
	}
	if d := inner.GetDeviceSentMessage(); d.GetMessage() != nil {
		inner = d.GetMessage()
	}
	if v := inner.GetViewOnceMessage(); v.GetMessage() != nil {
		inner = v.GetMessage()
	}
	if v := inner.GetViewOnceMessageV2(); v.GetMessage() != nil {
		inner = v.GetMessage()
	}
	// Tipe dengan hide SAH → bukan sPR.
	if inner.GetReactionMessage() != nil || inner.GetEncReactionMessage() != nil {
		return false
	}
	if inner.GetPollUpdateMessage() != nil {
		return false
	}
	if inner.GetProtocolMessage() != nil || inner.GetEditedMessage() != nil {
		return false
	}
	// Konten biasa?
	switch {
	case inner.GetConversation() != "":
		return true
	case inner.GetExtendedTextMessage() != nil,
		inner.GetImageMessage() != nil,
		inner.GetVideoMessage() != nil,
		inner.GetAudioMessage() != nil,
		inner.GetDocumentMessage() != nil,
		inner.GetStickerMessage() != nil,
		inner.GetPtvMessage() != nil,
		inner.GetDocumentWithCaptionMessage() != nil,
		inner.GetContactMessage() != nil,
		inner.GetContactsArrayMessage() != nil,
		inner.GetLocationMessage() != nil,
		inner.GetLiveLocationMessage() != nil,
		inner.GetEventMessage() != nil,
		inner.GetPollCreationMessage() != nil:
		return true
	}
	return false
}

// HandleUndecryptable dipanggil dari main eventHandler untuk tiap
// events.UndecryptableMessage. Mendeteksi & membongkar pesan sPR bila anti-sPR aktif.
func HandleUndecryptable(client *whatsmeow.Client, evt *events.UndecryptableMessage) {
	if client == nil || evt == nil {
		return
	}
	// Hanya relevan di grup.
	if !evt.Info.IsGroup {
		return
	}
	// Abaikan pesan dari bot sendiri (fitur sembunyi/premium/hidetagp bot juga
	// memakai decrypt-fail=hide — jangan menuduh diri sendiri).
	if evt.Info.IsFromMe {
		return
	}

	groupID := evt.Info.Chat.ToNonAD().String()
	sender := evt.Info.Sender
	failMode := string(evt.DecryptFailMode)
	isHide := evt.DecryptFailMode == events.DecryptFailHide

	// KUNCI kasus "target orang lain": bot bukan target → dikirimi SKDM (pkmsg) kunci
	// baru, TAPI gagal (sesi rusak) → whatsmeow proses pkmsg (fail="") & berhenti,
	// jadi DecryptFailMode event = "" walau node yang SAMA memuat <enc type=skmsg
	// decrypt-fail=hide>. Scan SEMUA enc di node mentah menangkap hide itu.
	rawHide := false
	if raw, ok := src.LookupRawNode(evt.Info.ID); ok && raw.EncHideAny && raw.HideEncType == "skmsg" {
		rawHide = true
	}

	// Rekam ke ring-buffer + echo debug SELALU (instrumentasi, tak bergantung toggle
	// alert) — supaya user bisa reproduksi bareng teman & lihat field asli.
	verdict := "desync?"
	switch {
	case isHide:
		verdict = "hide"
	case rawHide:
		verdict = "hide-raw" // hide ada di skmsg node walau enc pertama (pkmsg) yg gagal
	case evt.IsUnavailable:
		verdict = "no-resend?"
	}
	recordAntiSPRDump(antisprDumpEntry{
		When:            time.Now(),
		Group:           evt.Info.Chat.User,
		SenderUser:      sender.User,
		SenderDevice:    sender.Device,
		MsgID:           evt.Info.ID,
		IsUnavailable:   evt.IsUnavailable,
		UnavailableType: string(evt.UnavailableType),
		FailMode:        failMode,
		Verdict:         verdict,
	})
	fmt.Printf("[ANTISPR] undecryptable grup=%s pengirim=%s dev=%d id=%q unavail=%v utype=%q failmode=%q → %s\n",
		evt.Info.Chat.User, sender.User, sender.Device, evt.Info.ID,
		evt.IsUnavailable, evt.UnavailableType, failMode, verdict)

	if isGroupAntiSPRDebug(groupID) {
		echoAntiSPRDebug(client, evt, verdict)
	}

	// Alert hanya bila anti-sPR AKTIF di grup.
	if !src.DB.IsGroupAntiSPR(groupID) {
		return
	}

	// UnavailableType view_once = fitur WA resmi (pesan sekali-lihat), BUKAN sPR.
	if evt.UnavailableType == events.UnavailableTypeViewOnce {
		return
	}

	switch {
	case isHide:
		// Jalur (1): decrypt-fail=hide pada enc yang diproses → sinyal sPR kuat.
		fireAntiSPRAlert(client, evt, "hide")
	case rawHide:
		// Jalur (1b): node memuat skmsg ber-hide walau enc pertama (pkmsg SKDM) yg
		// gagal → sPR menyembunyikan orang lain (bot bukan target). Alert langsung.
		fireAntiSPRAlert(client, evt, "hide-raw")
	case evt.IsUnavailable:
		// Jalur (2): skmsg gagal-dekripsi tanpa hide → tunda; konfirmasi bila tak
		// di-resend dalam antisprConfirmWindow (membedakan sPR dari desync normal).
		scheduleAntiSPRConfirm(client, evt)
	}
}

// scheduleAntiSPRConfirm menunda alert jalur-(2). Bila (grup|ID) tak muncul sebagai
// pesan terdekripsi dalam antisprConfirmWindow → konfirmasi sPR → alert.
func scheduleAntiSPRConfirm(client *whatsmeow.Client, evt *events.UndecryptableMessage) {
	groupID := evt.Info.Chat.ToNonAD().String()
	pkey := groupID + "|" + evt.Info.ID

	antisprPendingMu.Lock()
	if antisprPending[pkey] {
		antisprPendingMu.Unlock()
		return // sudah dijadwalkan
	}
	antisprPending[pkey] = true
	antisprPendingMu.Unlock()

	time.AfterFunc(antisprConfirmWindow, func() {
		antisprPendingMu.Lock()
		delete(antisprPending, pkey)
		antisprPendingMu.Unlock()

		// Apakah pesan ini akhirnya di-resend & terdekripsi? → desync normal, batal.
		antisprDecryptedMu.Lock()
		_, resent := antisprDecrypted[pkey]
		antisprDecryptedMu.Unlock()
		if resent {
			fmt.Printf("[ANTISPR] id=%q di-resend dalam %s → desync normal, BUKAN sPR\n",
				evt.Info.ID, antisprConfirmWindow)
			return
		}
		// Toggle bisa saja dimatikan selama menunggu.
		if !src.DB.IsGroupAntiSPR(groupID) {
			return
		}
		fireAntiSPRAlert(client, evt, "no-resend")
	})
}

// fireAntiSPRAlert mengirim alert tag-pengirim ke grup (dengan cooldown per pengirim).
func fireAntiSPRAlert(client *whatsmeow.Client, evt *events.UndecryptableMessage, reason string) {
	fireAntiSPRAlertJID(client, evt.Info.Chat, evt.Info.Sender, evt.Info.ID, reason)
}

// fireAntiSPRAlertJID = inti alert; dipakai jalur undecryptable maupun jalur
// pesan-terbaca-ber-hide (bot bukan target).
func fireAntiSPRAlertJID(client *whatsmeow.Client, chat, sender types.JID, msgID, reason string) {
	groupID := chat.ToNonAD().String()

	// Dedup per (grup, message-ID): tiap pesan sPR di-alert tepat sekali. Pesan sPR
	// BARU (ID baru) langsung lolos (tak tertahan cooldown pengirim).
	key := groupID + "|" + msgID
	now := time.Now()
	antisprSeenMu.Lock()
	if last, ok := antisprSeen[key]; ok && now.Sub(last) < antisprDedupTTL {
		antisprSeenMu.Unlock()
		return
	}
	antisprSeen[key] = now
	// GC ringan: buang entri kedaluwarsa saat map membengkak.
	if len(antisprSeen) > 512 {
		for k, t := range antisprSeen {
			if now.Sub(t) > antisprDedupTTL {
				delete(antisprSeen, k)
			}
		}
	}
	antisprSeenMu.Unlock()

	fmt.Printf("[ANTISPR] ⚠️ ALERT grup=%s pengirim=%s id=%q alasan=%s → pesan tersembunyi (sPR) terdeteksi\n",
		chat.User, sender.User, msgID, reason)

	// Alert: tag pengirim di grup. Pakai bentuk yang bisa di-mention (sender @grup).
	mentionJID := sender.ToNonAD()
	text := fmt.Sprintf(
		"🕵️ *Pesan Tersembunyi Terdeteksi (anti-sPR)*\n\n"+
			"@%s barusan mengirim pesan yang *sengaja disembunyikan* dari sebagian anggota grup (teknik sPR/bisik).\n\n"+
			"_Isi pesannya tidak bisa dibaca semua orang — hati-hati._",
		mentionJID.User)

	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waProto.ContextInfo{
				MentionedJID: []string{mentionJID.String()},
			},
		},
	}
	if _, err := client.SendMessage(context.Background(), chat, msg, AndroidExtra()); err != nil {
		fmt.Printf("[ANTISPR] ⚠️ gagal kirim alert di %s: %v\n", chat.User, err)
	}
}

// echoAntiSPRDebug meng-echo field mentah event ke grup (mode `antispr debug on`)
// untuk reproduksi bareng teman — melihat persis apa yang dipancarkan whatsmeow.
func echoAntiSPRDebug(client *whatsmeow.Client, evt *events.UndecryptableMessage, verdict string) {
	sender := evt.Info.Sender
	text := fmt.Sprintf(
		"🐞 *anti-sPR DEBUG*\n"+
			"pengirim: @%s (dev %d)\n"+
			"id: `%s`\n"+
			"IsUnavailable: %v\n"+
			"UnavailableType: %q\n"+
			"DecryptFailMode: %q\n"+
			"verdict: %s",
		sender.User, sender.Device, evt.Info.ID,
		evt.IsUnavailable, string(evt.UnavailableType), string(evt.DecryptFailMode), verdict)
	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waProto.ContextInfo{
				MentionedJID: []string{sender.ToNonAD().String()},
			},
		},
	}
	if _, err := client.SendMessage(context.Background(), evt.Info.Chat, msg, AndroidExtra()); err != nil {
		fmt.Printf("[ANTISPR] ⚠️ gagal kirim debug-echo di %s: %v\n", evt.Info.Chat.User, err)
	}
}

func recordAntiSPRDump(e antisprDumpEntry) {
	antisprRingMu.Lock()
	antisprRing = append(antisprRing, e)
	if len(antisprRing) > antisprLogMax {
		antisprRing = antisprRing[len(antisprRing)-antisprLogMax:]
	}
	antisprRingMu.Unlock()
}

func isGroupAntiSPRDebug(groupID string) bool {
	antisprDebugMu.RLock()
	defer antisprDebugMu.RUnlock()
	return antisprDebug[groupID]
}

// ====================== COMMAND: TOGGLE ======================

func ExecuteAntiSPRToggle(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin && !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur anti-sPR.")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	sub := strings.ToLower(strings.TrimSpace(ctx.Args))

	switch sub {
	case "on":
		src.DB.SetGroupAntiSPR(groupID, true)
		return ctx.Reply("🕵️ *Anti-sPR AKTIF*.\n\nBot akan membongkar (tag) siapa pun yang mengirim pesan tersembunyi di grup ini.")
	case "off":
		src.DB.SetGroupAntiSPR(groupID, false)
		return ctx.Reply("🕵️ *Anti-sPR NONAKTIF*.")
	case "status", "show":
		return antisprStatus(ctx, groupID)
	case "log", "dump":
		return antisprShowLog(ctx, groupID)
	case "debug on", "debug":
		antisprDebugMu.Lock()
		antisprDebug[groupID] = true
		antisprDebugMu.Unlock()
		return ctx.Reply("🐞 *Anti-sPR DEBUG AKTIF* di grup ini.\n\nSetiap pesan gagal-dekripsi akan di-echo ke grup (field mentah). Minta teman kirim sPR sekarang, lalu `antispr log`.\n\nMatikan: `antispr debug off`.")
	case "debug off":
		antisprDebugMu.Lock()
		delete(antisprDebug, groupID)
		antisprDebugMu.Unlock()
		return ctx.Reply("🐞 Anti-sPR DEBUG nonaktif.")
	default:
		return ctx.Reply(antisprHelp())
	}
}

func antisprStatus(ctx *ContextBot, groupID string) error {
	status := "❌ OFF"
	if src.DB.IsGroupAntiSPR(groupID) {
		status = "✅ ON"
	}
	dbg := "off"
	if isGroupAntiSPRDebug(groupID) {
		dbg = "on"
	}
	return ctx.Reply(fmt.Sprintf(
		"🕵️ *Anti-sPR*\n\nStatus : %s\nDebug  : %s\n\n_Mendeteksi pesan tersembunyi (sPR/bisik) yang menyembunyikan bot, lalu men-tag pengirimnya._\n_Bantuan: `antispr help`._",
		status, dbg))
}

// antisprShowLog menampilkan dump undecryptable terakhir di grup ini.
func antisprShowLog(ctx *ContextBot, groupID string) error {
	groupUser := ctx.ChatJID.ToNonAD().User
	antisprRingMu.Lock()
	entries := make([]antisprDumpEntry, 0, len(antisprRing))
	for _, e := range antisprRing {
		if e.Group == groupUser {
			entries = append(entries, e)
		}
	}
	antisprRingMu.Unlock()

	if len(entries) == 0 {
		return ctx.Reply("📭 Belum ada pesan gagal-dekripsi tercatat di grup ini.\n\n_Aktifkan `antispr debug on`, minta teman kirim sPR, lalu cek `antispr log` lagi._")
	}

	var b strings.Builder
	b.WriteString("📓 *Anti-sPR — log undecryptable*\n")
	b.WriteString(fmt.Sprintf("_%d entri terakhir (terbaru di bawah)_\n\n", len(entries)))
	for _, e := range entries {
		b.WriteString(fmt.Sprintf(
			"• %s | %s (dev %d)\n  id=`%s`\n  unavail=%v utype=%q fail=%q → *%s*\n",
			e.When.Format("15:04:05"), e.SenderUser, e.SenderDevice, e.MsgID,
			e.IsUnavailable, e.UnavailableType, e.FailMode, e.Verdict))
	}
	return ctx.Reply(b.String())
}

func antisprHelp() string {
	return "📖 *Anti-sPR* (deteksi pesan tersembunyi/bisik)\n\n" +
		"`antispr on` — aktifkan (tag pengirim pesan sPR)\n" +
		"`antispr off` — matikan\n" +
		"`antispr status` — lihat status\n" +
		"`antispr log` — lihat log pesan gagal-dekripsi\n" +
		"`antispr debug on/off` — echo field mentah ke grup (untuk repro bareng teman)\n\n" +
		"_Deteksi 2 jalur: (1) decrypt-fail=hide, (2) skmsg gagal-dekripsi yang tak pernah di-resend._\n" +
		"_Catatan: hanya mendeteksi sPR yang menyembunyikan bot (bot harus jadi anggota grup)._"
}
