package commands

import (
	"context"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// =================================================================
// FITUR: HAPUS PESAN VIA REAKSI (bukan command teks — dipicu event reaksi).
//
// Bila OWNER atau ADMIN grup mengirim REAKSI (emoji hapus: 🗑️ / ❌ / 🚮) ke
// sebuah pesan:
//   • Pesan itu dari BOT      → bot menghapus (revoke) pesannya sendiri.
//   • Pesan dari orang lain   → bila BOT admin grup, bot menghapus pesan itu.
//
// Reaksi biasa (👍, ❤️, dst.) diabaikan agar tak ada penghapusan tak sengaja.
// Dipanggil dari eventHandler main.go untuk tiap events.Message ber-ReactionMessage.
// =================================================================

// deleteReactionEmojis: hanya emoji ini yang memicu penghapusan.
var deleteReactionEmojis = []string{"🗑", "❌", "🚮"}

func isDeleteReaction(text string) bool {
	for _, e := range deleteReactionEmojis {
		if strings.Contains(text, e) {
			return true
		}
	}
	return false
}

// HandleReactionDelete memproses reaksi masuk untuk fitur hapus-via-reaksi.
func HandleReactionDelete(client *whatsmeow.Client, evt *events.Message) {
	if evt == nil || evt.Message == nil {
		return
	}
	// Reaksi bot sendiri (mis. ack ⏳/✅) tak boleh memicu apa pun.
	if evt.Info.IsFromMe {
		return
	}

	rm := evt.Message.GetReactionMessage()
	if rm == nil {
		return
	}
	key := rm.GetKey()
	if key == nil || key.GetID() == "" {
		return
	}
	// Reaksi kosong = reaksi DICABUT → abaikan. Hanya emoji hapus yang memicu.
	if !isDeleteReaction(rm.GetText()) {
		return
	}

	// Siapa yang bereaksi (untuk cek owner/admin).
	reactorUser := evt.Info.Sender.ToNonAD().User
	reactorAlt := evt.Info.MessageSource.SenderAlt.ToNonAD().User
	isOwner := false
	if src.AppConfig != nil {
		isOwner = reactorUser == src.AppConfig.OwnerNumber || reactorAlt == src.AppConfig.OwnerNumber
	}

	// ContextBot minimal supaya bisa pakai helper admin (isUserAdmin/botIsGroupAdmin).
	ctx := &ContextBot{
		Client:    client,
		Msg:       evt,
		ChatJID:   evt.Info.Chat,
		SenderJID: evt.Info.Sender,
		SenderAlt: evt.Info.MessageSource.SenderAlt,
		IsOwner:   isOwner,
		IsGroup:   evt.Info.IsGroup,
		Ctx:       context.Background(),
	}

	// Otorisasi pelaku: owner selalu boleh; selain itu wajib admin grup.
	if !isOwner {
		if !evt.Info.IsGroup {
			return // di japri hanya owner
		}
		if admin, _ := isUserAdmin(ctx); !admin {
			return
		}
	}

	chat := evt.Info.Chat
	targetID := key.GetID()

	// Kasus 1: target adalah pesan BOT sendiri → revoke pesan sendiri (EmptyJID).
	if key.GetFromMe() {
		revoke := client.BuildRevoke(chat, types.EmptyJID, targetID)
		if _, err := client.SendMessage(context.Background(), chat, revoke, src.AndroidExtra()); err != nil {
			src.Print("[reactdelete] gagal hapus pesan bot: %v", err)
		}
		return
	}

	// Kasus 2: target pesan orang lain → butuh BOT admin grup.
	if !evt.Info.IsGroup {
		return // tak bisa menghapus pesan orang lain di japri
	}
	if !botIsGroupAdmin(ctx, chat.ToNonAD().String()) {
		return // bot bukan admin → tak bisa menghapus; diam
	}

	// Pengirim asli pesan target ada di participant key reaksi.
	sender := types.EmptyJID
	if p := key.GetParticipant(); p != "" {
		if j, err := types.ParseJID(p); err == nil {
			sender = j
		}
	}
	if sender.IsEmpty() {
		return // tanpa participant tak bisa revoke pesan orang lain
	}

	revoke := client.BuildRevoke(chat, sender, targetID)
	if _, err := client.SendMessage(context.Background(), chat, revoke, src.AndroidExtra()); err != nil {
		src.Print("[reactdelete] gagal hapus pesan member: %v", err)
	}
}
