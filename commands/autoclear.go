package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// Command `autoclear` (owner) — kontrol fitur auto-clear chat berkala.
// Tiap N menit bot menghapus chat aktif dari TAMPILAN akun bot (kecuali owner),
// tersinkron ke perangkat tertaut bot. Tidak menghapus pesan untuk orang lain.
func init() {
	RegisterCommand(Command{
		Name:        "AutoClear",
		Category:    "Owner",
		Aliases:     []string{"autoclear", "autoclearchat"},
		Pattern:     regexp.MustCompile(`(?i)^\s*autoclear(?:chat)?(?:\s+(on|off|status|show))?\s*$`),
		Description: "[Owner] Atur auto-clear chat berkala: on/off/status",
		Execute:     ExecuteAutoClear,
	})
}

func ExecuteAutoClear(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa mengatur auto-clear.")
	}
	sub := strings.ToLower(strings.TrimSpace(ctx.Args))

	mins := 30
	if src.AppConfig != nil && src.AppConfig.AutoClearMinutes > 0 {
		mins = src.AppConfig.AutoClearMinutes
	}

	switch sub {
	case "on":
		src.SetAutoClear(true)
		return ctx.Reply(fmt.Sprintf("🧹 *Auto-clear chat AKTIF* — tiap %d menit chat dibersihkan dari tampilan bot (kecuali owner).", mins))
	case "off":
		src.SetAutoClear(false)
		return ctx.Reply("🧹 *Auto-clear chat NONAKTIF.*")
	default: // status / show / kosong
		state := "❌ OFF"
		if src.AutoClearOn() {
			state = "✅ ON"
		}
		return ctx.Reply(fmt.Sprintf(
			"🧹 *Auto-Clear Chat*\n\nStatus   : %s\nInterval : %d menit\nKecuali  : chat owner\n\n_Hapus chat dari tampilan akun bot (sinkron ke perangkat tertaut bot). Tidak menghapus pesan untuk lawan bicara._\n\n`autoclear on` / `autoclear off`",
			state, mins))
	}
}
