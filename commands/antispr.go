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
// Tanda tangan sPR (yang membedakannya dari desync sender-key biasa):
//   • DecryptFailMode == "hide"  → pengirim SENGAJA menandai node skmsg dengan
//     atribut decrypt-fail=hide supaya device yang gagal dekripsi TIDAK menampilkan
//     placeholder "Menunggu pesan ini" (melainkan KOSONG). Inilah tepat yang
//     dilakukan `sembunyi`/premium/sPR Baileys modern. App WA resmi hanya memakai
//     "hide" untuk edit/reaction/poll — dan tipe-tipe itu normalnya BISA didekripsi,
//     jadi tak sampai memancarkan UndecryptableMessage. Maka kombinasi
//     "grup + gagal dekripsi + decrypt-fail=hide" = sinyal sPR yang kuat.
//
// Bila anti-sPR AKTIF di grup (command `antispr on`), bot membalas dengan menandai
// (mention) pengirim: membongkar pelaku secara publik.
//
// Diaktifkan per-grup (default OFF). Owner/admin yang mengatur.
// =================================================================

const (
	// antisprAlertCooldown mencegah spam: satu aksi sPR bisa memicu beberapa
	// UndecryptableMessage (retry-receipt bisa mengulang event). Satu alert per
	// (grup, pengirim) tiap jendela ini.
	antisprAlertCooldown = 45 * time.Second
)

var (
	antisprSeen   = make(map[string]time.Time) // key = grup|pengirim → waktu alert terakhir
	antisprSeenMu sync.Mutex
)

var reAntiSPRArgs = regexp.MustCompile(`(?i)^\s*antispr(?:\s+(.+))?\s*$`)

func init() {
	RegisterCommand(Command{
		Name:        "Anti-SPR",
		Category:    "Group",
		Aliases:     []string{"antispr", "antibisik", "antisembunyi"},
		Pattern:     reAntiSPRArgs,
		Description: "Deteksi pesan tersembunyi (sPR) di grup → alert tag pengirim: on/off/status/help (admin)",
		Execute:     ExecuteAntiSPRToggle,
	}).Use(GroupOnlyMiddleware)
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
	// Tanda tangan sPR: pengirim menandai node skmsg dengan decrypt-fail=hide.
	// (Desync sender-key biasa TIDAK memakai hide → placeholder biasa, bukan sPR.)
	if evt.DecryptFailMode != events.DecryptFailHide {
		return
	}

	groupID := evt.Info.Chat.ToNonAD().String()
	if !src.DB.IsGroupAntiSPR(groupID) {
		return
	}

	// Rate-limit per (grup, pengirim).
	sender := evt.Info.Sender
	key := groupID + "|" + sender.ToNonAD().User
	now := time.Now()
	antisprSeenMu.Lock()
	if last, ok := antisprSeen[key]; ok && now.Sub(last) < antisprAlertCooldown {
		antisprSeenMu.Unlock()
		return
	}
	antisprSeen[key] = now
	antisprSeenMu.Unlock()

	fmt.Printf("[ANTISPR] grup=%s pengirim=%s id=%q → pesan tersembunyi (sPR) terdeteksi (decrypt-fail=hide)\n",
		evt.Info.Chat.User, sender.User, evt.Info.ID)

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
	if _, err := client.SendMessage(context.Background(), evt.Info.Chat, msg, AndroidExtra()); err != nil {
		fmt.Printf("[ANTISPR] ⚠️ gagal kirim alert di %s: %v\n", evt.Info.Chat.User, err)
	}
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
	default:
		return ctx.Reply(antisprHelp())
	}
}

func antisprStatus(ctx *ContextBot, groupID string) error {
	status := "❌ OFF"
	if src.DB.IsGroupAntiSPR(groupID) {
		status = "✅ ON"
	}
	return ctx.Reply(fmt.Sprintf(
		"🕵️ *Anti-sPR*\n\nStatus : %s\n\n_Mendeteksi pesan tersembunyi (sPR/bisik) yang menyembunyikan bot, lalu men-tag pengirimnya._\n_Bantuan: `antispr help`._",
		status))
}

func antisprHelp() string {
	return "📖 *Anti-sPR* (deteksi pesan tersembunyi/bisik)\n\n" +
		"`antispr on` — aktifkan (tag pengirim pesan sPR)\n" +
		"`antispr off` — matikan\n" +
		"`antispr status` — lihat status\n\n" +
		"_Catatan: hanya mendeteksi pesan sPR yang menyembunyikan bot (bot harus jadi anggota grup)._"
}
