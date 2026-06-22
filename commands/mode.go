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
		Pattern:     regexp.MustCompile(`(?i)^\s*(self|public)\s*$`),
		Description: "Atur mode grup INI: public (semua) / self (hanya owner)",
		Execute:     ExecuteBotMode,
	}).Use(OwnerOnlyMiddleware).Use(GroupOnlyMiddleware)

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
	groupID := ctx.ChatJID.ToNonAD().String()
	choice := strings.ToLower(strings.TrimSpace(ctx.TextMessage))

	if strings.HasPrefix(choice, "self") {
		src.DB.SetGroupSelf(groupID, true)
		return ctx.Reply("🔒 Grup ini → mode *SELF*. Hanya owner yang dilayani di sini.")
	}
	src.DB.SetGroupSelf(groupID, false)
	return ctx.Reply("🌐 Grup ini → mode *PUBLIC*. Semua member dilayani.")
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
			"⌨️ Mode *PREFIX* aktif.\nNon-owner WAJIB pakai prefix; owner bebas.\nPrefix yang diterima: `%s`\nContoh: `%smenu`",
			pc, first))
	}
	return ctx.Reply("⌨️ Mode *NO-PREFIX* aktif. Semua user tanpa prefix.")
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
