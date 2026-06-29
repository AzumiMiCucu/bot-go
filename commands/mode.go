package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// MODE BOT: self/public & prefix/no-prefix (khusus owner)
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Mode Self/Public",
		Category:    "Owner",
		Aliases:     []string{"self", "public"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(self|public)(?:\s+(help|status|show))?\s*$`),
		Description: "Atur mode: di grup → grup ini, di japri → chat pribadi · public/self · help/status",
		Execute:     ExecuteBotMode,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Mode Prefix",
		Category:    "Owner",
		Aliases:     []string{"prefix", "mode"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:prefix|mode)\s+(prefix|noprefix|on|off)\s*$`),
		Description: "Aktifkan/matikan mode prefix command (default no-prefix)",
		Execute:     ExecutePrefixMode,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Set Prefix Char",
		Category:    "Owner",
		Aliases:     []string{"setprefix"},
		Pattern:     regexp.MustCompile(`(?i)^\s*setprefix\s+(\S+)\s*$`),
		Description: "Ubah karakter prefix (mis. setprefix !)",
		Execute:     ExecuteSetPrefix,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteBotMode(ctx *ContextBot) error {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(ctx.TextMessage)))
	mode, arg := "", ""
	if len(fields) > 0 {
		mode = fields[0]
	}
	if len(fields) > 1 {
		arg = fields[1]
	}
	isStatus := arg == "help" || arg == "status" || arg == "show"

	// SCOPE GRUP — berlaku hanya untuk grup ini (DEFAULT grup = SELF).
	if ctx.IsGroup {
		groupID := ctx.ChatJID.ToNonAD().String()
		if isStatus {
			cur := "🌐 PUBLIC (semua member dilayani)"
			if src.DB.IsGroupSelf(groupID) {
				cur = "🔒 SELF (hanya owner dilayani)"
			}
			return ctx.Reply(fmt.Sprintf(
				"⚙️ *Mode Grup Ini*\n\nStatus : %s\n", cur))
		}
		if mode == "self" {
			src.DB.SetGroupSelf(groupID, true)
			return ctx.Reply("🔒 Grup ini → mode *SELF*. Hanya owner yang dilayani di sini.")
		}
		src.DB.SetGroupSelf(groupID, false)
		return ctx.Reply("🌐 Grup ini → mode *PUBLIC*. Semua member dilayani.")
	}

	// SCOPE CHAT PRIBADI (japri) — berlaku untuk SEMUA japri (DEFAULT = PUBLIC).
	if isStatus {
		cur := "🌐 PUBLIC (semua chat pribadi dilayani)"
		if src.IsPrivateSelf() {
			cur = "🔒 SELF (hanya owner dilayani)"
		}
		return ctx.Reply(fmt.Sprintf(
			"⚙️ *Mode Chat Pribadi*\n\nStatus : %s\n", cur))
	}
	if mode == "self" {
		src.SetPrivateSelf(true)
		return ctx.Reply("🔒 Chat pribadi → mode *SELF*. Hanya owner yang dilayani di japri.")
	}
	src.SetPrivateSelf(false)
	return ctx.Reply("🌐 Chat pribadi → mode *PUBLIC*. Semua chat pribadi dilayani.")
}

func ExecutePrefixMode(ctx *ContextBot) error {
	arg := strings.ToLower(strings.TrimSpace(ctx.Args))
	on := arg == "prefix" || arg == "on"
	src.AppConfig.PrefixMode = on
	_ = src.SaveConfig()

	if on {
		pc := src.AppConfig.PrefixChar
		if pc == "" {
			pc = "."
		}
		first := string([]rune(pc)[0])
		return ctx.Reply(fmt.Sprintf(
			"⌨️ Mode *PREFIX* aktif.`"))
	}
	return ctx.Reply("⌨️ Mode *NO-PREFIX* aktif.")
}

func ExecuteSetPrefix(ctx *ContextBot) error {
	// Boleh lebih dari satu karakter — tiap karakter jadi prefix yang valid.
	p := strings.TrimSpace(ctx.Args)
	p = strings.ReplaceAll(p, " ", "")
	if p == "" || len(p) > 10 {
		return ctx.Reply("⚠️ Prefix tidak valid (1-10 karakter). Contoh: `setprefix .` atau `setprefix .!#/`")
	}
	src.AppConfig.PrefixChar = p
	_ = src.SaveConfig()
	return ctx.Reply(fmt.Sprintf("✅ Prefix sekarang: `%s` (tiap karakter berlaku sebagai prefix)", p))
}
