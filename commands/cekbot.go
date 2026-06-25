package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

func init() {
	RegisterCommand(Command{
		Name:        "CekBot",
		Category:    "Owner",
		Aliases:     []string{"cekbot", "isbot"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(cekbot|isbot)\s*$`),
		Description: "[Owner] Deteksi bot/Baileys dari pesan yang di-reply",
		Execute:     ExecuteCekBot,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteCekBot(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa menggunakan fitur ini.")
	}

	// Ambil pesan yang di-reply (bila ada), jika tidak → analisis pesan ini sendiri
	targetMsg := ctx.Msg
	targetUser := ctx.User
	targetPushName := ctx.PushName

	if ctxInfo := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo(); ctxInfo != nil {
		if qm := ctxInfo.GetQuotedMessage(); qm != nil {
			// User me-reply pesan lain: analisis pesan yang di-reply lewat StanzaID,
			// participant, DAN isi quoted (yang kadang masih membawa metadata mentah).
			// Semua logika deteksi berada di system (src), command hanya menyajikan.
			stanzaID := ctxInfo.GetStanzaID()
			participant := ctxInfo.GetParticipant()
			if stanzaID != "" {
				// Utamakan analisis LIVE yang ditangkap saat pesan tiba (metadata raw
				// asli). Bila pesan terlalu lama / tak tertangkap → fallback ke quote.
				if live, ok := src.LookupDetection(stanzaID); ok {
					return showDetection(ctx, &live, live.SenderUser, live.PushName, "live")
				}
				det := src.DetectBotFromQuoted(stanzaID, participant, qm)
				return showDetection(ctx, &det, det.SenderUser, "", "terbatas")
			}
		}
	}

	// Analisis penuh pesan saat ini (yang memicu command)
	// Tapi kita ingin menganalisis PENGIRIM LAIN, bukan owner sendiri.
	// Jika owner mengirim .cekbot (tanpa reply), analisis pesan owner = tidak berguna.
	if targetUser == src.AppConfig.OwnerNumber {
		return ctx.Reply("📌 *Cara pakai:* reply pesan yang mencurigakan dengan `cekbot`\n\nBot akan menganalisis message-ID, sinyal Baileys, metadata device, dan banyak lagi.")
	}

	det := src.DetectBot(targetMsg)
	return showDetection(ctx, &det, targetUser, targetPushName, "live")
}

// showDetection menyajikan hasil deteksi secara RINGKAS, berpusat pada verdict
// (gaya khas proyek ini — BUKAN layout Target/Engine/Detection). source: "live"
// (metadata raw asli, akurat) atau "terbatas" (hanya dari quote).
func showDetection(ctx *ContextBot, det *src.BotDetectionResult, user, pushName, source string) error {
	sb := strings.Builder{}
	sb.WriteString("*「 ISBOT CHECKER 」*\n\n")

	// ── Verdict (headline) ──
	icon, label := verdictBadge(det.Verdict)
	sb.WriteString(fmt.Sprintf("%s *%s*\n", icon, label))

	// ── Pengirim · platform ──
	who := "@" + user
	if pushName != "" {
		who = pushName + " (@" + user + ")"
	}
	dev := det.DeviceClass
	if dev == "" {
		dev = "?"
	}
	sb.WriteString(fmt.Sprintf("%s · %s\n", who, dev))

	// ── Device-index: pembeda primary/secondary yang sebenarnya ──
	// device 0 = HP utama (manusia); device != 0 = companion (WA Web/Desktop/bot).
	if source == "terbatas" {
		sb.WriteString(fmt.Sprintf("📱 device-index: %d\n\n", det.DeviceID))
	} else if det.IsPrimary {
		sb.WriteString("📱 device 0 — *HP utama (primary)*\n\n")
	} else {
		sb.WriteString(fmt.Sprintf("🔗 device %d — *companion (secondary)*\n\n", det.DeviceID))
	}

	// ── Sinyal kunci (maks beberapa baris) ──
	if source == "terbatas" {
		sb.WriteString("⚠️ _Pesan tak tertangkap live — metadata raw tak lengkap, hasil terbatas._\n")
	} else {
		// isUseDevice: pembeda kunci WA Web resmi vs Baileys lama (keduanya bisa 3EB0).
		if det.IsUseDevice {
			enc := "E2EE"
			if det.IsHostedEncryption {
				enc = "HOSTED"
			}
			sb.WriteString(fmt.Sprintf("✅ multi-device aktif (%s)\n", enc))
		} else if det.IsSecondary {
			sb.WriteString("❌ tanpa metadata multi-device\n")
		}
		if det.IsFromBotServer {
			sb.WriteString("⚠️ Akun/server bot WhatsApp\n")
		} else if det.HasBotMetadata || det.HasBotSecret || det.IsBotInvoke {
			sb.WriteString("↪️ Metadata thread-bot\n")
		}
		// Pisahkan sidik jari ID: BAE* = Baileys pasti; 3EB0 = ambigu.
		if det.StrongBaileys {
			sb.WriteString(fmt.Sprintf("⚠️ ID Baileys (%s) — sidik jari kuat\n", det.BaileysIDPrefix))
		} else if det.IsBaileysID {
			sb.WriteString("⚠️ ID 3EB0 tanpa metadata device\n")
		} else if det.AmbiguousID {
			sb.WriteString("• ID 3EB0 + multi-device\n")
		}
	}

	// ── Alasan ringkas ──
	if det.VerdictReason != "" {
		sb.WriteString(fmt.Sprintf("_%s._\n", det.VerdictReason))
	}

	sb.WriteString(fmt.Sprintf("\n#%s", det.MessageID))

	return ctx.Reply(sb.String())
}

// verdictBadge memetakan verdict ke (ikon, label) — gaya bahasa khas proyek ini.
func verdictBadge(verdict string) (string, string) {
	switch verdict {
	case src.VerdictHuman:
		return "👤", "MANUSIA"
	case src.VerdictBot:
		return "🤖", "BOT"
	case src.VerdictBaileys:
		return "🔴", "BAILEYS"
	case src.VerdictSuspect:
		return "🟠", "MENCURIGAKAN"
	default:
		return "⚪", "BELUM PASTI"
	}
}
