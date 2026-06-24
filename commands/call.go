package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "Call",
		Category:    "Owner",
		Aliases:     []string{"call", "telepon", "panggil"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:call|telepon|panggil)\s*(.*)$`),
		Description: "[Owner] Telepon nomor/orang via VoIP WhatsApp. Opsional putar audio: call <nomor> <file.mp3>",
		Execute:     ExecuteCall,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Hangup",
		Category:    "Owner",
		Aliases:     []string{"hangup", "tutup", "endcall"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(hangup|tutup|endcall)\s*$`),
		Description: "[Owner] Tutup semua panggilan yang sedang berlangsung",
		Execute:     ExecuteHangup,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "AntiCall",
		Category:    "Owner",
		Aliases:     []string{"anticall"},
		Pattern:     regexp.MustCompile(`(?i)^\s*anticall\s*(on|off)?\s*$`),
		Description: "[Owner] Tolak otomatis panggilan masuk ke bot (anticall on|off)",
		Execute:     ExecuteAntiCall,
	}).Use(OwnerOnlyMiddleware)
}

// ExecuteCall menelepon target dan (opsional) memutar file audio.
//
// Pemakaian:
//   - call <nomor> [file_audio]   → telepon nomor, opsional putar audio
//   - reply pesan + call [file]   → telepon pengirim pesan yang di-reply
func ExecuteCall(ctx *ContextBot) error {
	if src.CallClient == nil {
		return ctx.Reply("⚠️ Subsistem panggilan belum aktif.")
	}

	target := ""
	audioPath := ""

	// Mode reply: telepon pengirim pesan yang di-reply.
	if ctxInfo := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo(); ctxInfo != nil && ctxInfo.GetParticipant() != "" {
		target = ctxInfo.GetParticipant()
		audioPath = strings.TrimSpace(ctx.Args) // semua arg dianggap path audio
	} else {
		// Mode argumen: <nomor> [file_audio]
		fields := strings.Fields(ctx.Args)
		if len(fields) == 0 {
			return ctx.Reply("📞 *Cara pakai:*\n• `call <nomor>` — telepon nomor\n• `call <nomor> <file.mp3>` — telepon + putar audio\n• reply pesan lalu `call` — telepon pengirimnya")
		}
		target = fields[0]
		if len(fields) > 1 {
			audioPath = strings.Join(fields[1:], " ")
		}
	}

	_ = ctx.React("📞")

	// notify melaporkan progres panggilan ke chat (dipanggil dari goroutine library).
	chatJID := ctx.ChatJID
	client := ctx.Client
	notify := func(label string) {
		var text string
		switch {
		case label == "ringing":
			text = "📲 Berdering..."
		case label == "active":
			text = "🟢 Tersambung!"
		case strings.HasPrefix(label, "ended"):
			reason := strings.TrimPrefix(label, "ended:")
			if reason == "" || reason == "ended" {
				text = "🔴 Panggilan berakhir."
			} else {
				text = "🔴 Panggilan berakhir (" + reason + ")."
			}
		default:
			return // fase lain (calling/connecting) tak perlu dilaporkan
		}
		_, _ = client.SendMessage(context.Background(), chatJID, &waProto.Message{
			Conversation: proto.String(text),
		}, src.AndroidExtra())
	}

	callID, peer, err := src.StartCall(ctx.Ctx, target, audioPath, notify)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal menelepon: " + err.Error())
	}

	msg := fmt.Sprintf("☎️ *Memanggil...*\n👤 %s", peer)
	if audioPath != "" {
		msg += fmt.Sprintf("\n🎵 Audio: %s (diputar saat tersambung)", audioPath)
	}
	msg += fmt.Sprintf("\n🆔 %s", callID)
	return ctx.Reply(msg)
}

// ExecuteHangup menutup semua panggilan aktif.
func ExecuteHangup(ctx *ContextBot) error {
	n := src.HangupAllCalls()
	if n == 0 {
		return ctx.Reply("🔕 Tidak ada panggilan aktif.")
	}
	return ctx.Reply(fmt.Sprintf("🔴 %d panggilan ditutup.", n))
}

// ExecuteAntiCall menyalakan/mematikan penolakan otomatis panggilan masuk.
func ExecuteAntiCall(ctx *ContextBot) error {
	arg := strings.ToLower(strings.TrimSpace(ctx.Args))

	if arg == "" {
		status := "OFF"
		if src.AppConfig.AntiCall {
			status = "ON"
		}
		return ctx.Reply(fmt.Sprintf("☎️ *Anti-Call:* %s\n\nGunakan `anticall on` / `anticall off`.", status))
	}

	switch arg {
	case "on":
		src.AppConfig.AntiCall = true
	case "off":
		src.AppConfig.AntiCall = false
	default:
		return ctx.Reply("⚠️ Pilihan tidak valid. Gunakan `anticall on` atau `anticall off`.")
	}

	if err := src.SaveConfig(); err != nil {
		return ctx.Reply("⚠️ Status diubah tetapi gagal menyimpan config: " + err.Error())
	}

	if src.AppConfig.AntiCall {
		return ctx.Reply("🚫 *Anti-Call AKTIF.* Semua panggilan masuk akan ditolak otomatis.")
	}
	return ctx.Reply("✅ *Anti-Call NONAKTIF.* Panggilan masuk dibiarkan.")
}
