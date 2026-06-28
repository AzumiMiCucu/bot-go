package commands

import (
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// REMINDER (owner only) — kontrol pengingat otomatis Google.
//
//	reminder         → status
//	reminder on/off  → aktif/nonaktif
//	reminder test    → kirim briefing sekarang
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Reminder",
		Category:    "Owner",
		Aliases:     []string{"reminder", "pengingat"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:reminder|pengingat)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Kontrol pengingat otomatis acara & tugas Google",
		Execute:     ExecuteReminder,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteReminder(ctx *ContextBot) error {
	switch strings.ToLower(strings.TrimSpace(ctx.Args)) {
	case "on", "aktif", "nyala":
		src.SetReminder(true)
		return ctx.Reply("✅ Reminder otomatis *AKTIF*. Bot akan japri pengingat acara & briefing pagi.")
	case "off", "mati", "nonaktif":
		src.SetReminder(false)
		return ctx.Reply("🔕 Reminder otomatis *NONAKTIF*.")
	case "test", "coba":
		_ = ctx.React("⏳")
		if err := src.TriggerMorningBrief(); err != nil {
			return ctx.Reply("❌ " + err.Error())
		}
		return ctx.React("✅")
	default:
		status := "NONAKTIF"
		if src.ReminderOn() {
			status = "AKTIF"
		}
		g := "belum terhubung"
		if src.GoogleReady() {
			g = "terhubung"
		}
		return ctx.Reply("⏰ *REMINDER OTOMATIS*\n\n" +
			"Status : *" + status + "*\n" +
			"Google : " + g + "\n\n" +
			"• Pengingat ~30 menit sebelum acara\n" +
			"• Briefing pagi (agenda + tugas)\n\n" +
			"`reminder on` / `reminder off` / `reminder test`")
	}
}
