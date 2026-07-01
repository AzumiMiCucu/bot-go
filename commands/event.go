package commands

import (
	"context"
	"regexp"
	"strings"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// COMMAND: EVENT & PIN — dua tipe pesan waE2E yang langka & jarang di-port.
//
//   event <judul> | <deskripsi> | <YYYY-MM-DD HH:MM> | <lokasi>
//        → kirim KARTU ACARA (EventMessage) ke grup/chat. Anggota bisa RSVP.
//        Hanya judul yang wajib; sisanya opsional (pisah dengan `|`).
//
//   pin  (reply sebuah pesan)  → PIN pesan itu untuk semua (PinInChatMessage).
//   unpin (reply)              → lepas pin.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Buat Event",
		Category:    "Tools",
		Aliases:     []string{"event", "acara"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:event|acara)\s+([\s\S]+)$`),
		Description: "Kirim kartu acara (EventMessage) — judul | deskripsi | tanggal | lokasi",
		Execute:     ExecuteEvent,
	})

	RegisterCommand(Command{
		Name:        "Pin Pesan",
		Category:    "Group",
		Aliases:     []string{"pin", "unpin"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(pin|unpin)\s*$`),
		Description: "[Reply] Pin / lepas-pin pesan untuk semua (PinInChatMessage)",
		Execute:     ExecutePin,
	})
}

func ExecuteEvent(ctx *ContextBot) error {
	raw := strings.TrimSpace(ctx.Args)
	parts := strings.Split(raw, "|")
	name := strings.TrimSpace(parts[0])
	if name == "" {
		return ctx.Reply("⚠️ Format: `event <judul> | <deskripsi> | <YYYY-MM-DD HH:MM> | <lokasi>`\n\nHanya judul yang wajib.\nContoh: `event Rapat Tim | Bahas rilis | 2026-07-05 19:30 | Zoom`")
	}

	ev := &waProto.EventMessage{
		Name:               proto.String(name),
		IsCanceled:         proto.Bool(false),
		ExtraGuestsAllowed: proto.Bool(true),
	}

	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		ev.Description = proto.String(strings.TrimSpace(parts[1]))
	}
	if len(parts) > 2 {
		if ts := parseEventTime(strings.TrimSpace(parts[2])); ts > 0 {
			ev.StartTime = proto.Int64(ts)
		}
	}
	if len(parts) > 3 && strings.TrimSpace(parts[3]) != "" {
		loc := strings.TrimSpace(parts[3])
		ev.Location = &waProto.LocationMessage{Name: proto.String(loc)}
	}

	_ = ctx.React("📅")
	_, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, &waProto.Message{
		EventMessage: ev,
	})
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengirim event: " + err.Error())
	}
	_ = ctx.React("✅")
	return nil
}

// parseEventTime menerima "YYYY-MM-DD HH:MM" (waktu lokal) → epoch detik.
func parseEventTime(s string) int64 {
	if s == "" {
		return 0
	}
	layouts := []string{"2006-01-02 15:04", "2006-01-02", "02/01/2006 15:04", "02-01-2006 15:04"}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func ExecutePin(ctx *ContextBot) error {
	// Ambil pesan yang di-reply (target pin).
	ci := extractContextInfo(ctx.Msg.Message)
	if ci == nil || ci.GetStanzaID() == "" {
		return ctx.Reply("⚠️ Reply pesan yang mau di-pin, lalu ketik `pin` (atau `unpin`).")
	}

	stanzaID := ci.GetStanzaID()
	sender := ctx.SenderJID
	if p := ci.GetParticipant(); p != "" {
		if j, err := types.ParseJID(p); err == nil {
			sender = j
		}
	}

	pinType := waProto.PinInChatMessage_PIN_FOR_ALL
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ctx.TextMessage)), "unpin") {
		pinType = waProto.PinInChatMessage_UNPIN_FOR_ALL
	}

	msg := &waProto.Message{
		PinInChatMessage: &waProto.PinInChatMessage{
			Key:               ctx.Client.BuildMessageKey(ctx.ChatJID, sender, stanzaID),
			Type:              pinType.Enum(),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	}

	_ = ctx.React("📌")
	_, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msg)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal pin: " + err.Error())
	}
	_ = ctx.React("✅")
	return nil
}
