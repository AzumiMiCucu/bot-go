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
		Description: "Atur bot melayani semua orang (public) atau hanya owner (self)",
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
	choice := strings.ToLower(strings.TrimSpace(ctx.TextMessage))
	if strings.HasPrefix(choice, "self") {
		src.AppConfig.BotMode = "self"
	} else {
		src.AppConfig.BotMode = "public"
	}
	_ = src.SaveConfig()

	if src.AppConfig.BotMode == "self" {
		return ctx.Reply("🔒 Mode *SELF* aktif. Bot hanya melayani owner.")
	}
	return ctx.Reply("🌐 Mode *PUBLIC* aktif. Bot melayani semua orang.")
}

func ExecutePrefixMode(ctx *ContextBot) error {
	arg := strings.ToLower(strings.TrimSpace(ctx.Args))
	on := arg == "prefix" || arg == "on"
	src.AppConfig.PrefixMode = on
	_ = src.SaveConfig()

	if on {
		return ctx.Reply(fmt.Sprintf("⌨️ Mode *PREFIX* aktif. Command wajib diawali `%s`\nContoh: `%smenu`",
			src.AppConfig.PrefixChar, src.AppConfig.PrefixChar))
	}
	return ctx.Reply("⌨️ Mode *NO-PREFIX* aktif (default). Command tanpa prefix.")
}

func ExecuteSetPrefix(ctx *ContextBot) error {
	p := strings.TrimSpace(ctx.Args)
	if p == "" || len(p) > 3 {
		return ctx.Reply("⚠️ Prefix tidak valid. Contoh: `setprefix !`")
	}
	src.AppConfig.PrefixChar = p
	_ = src.SaveConfig()
	return ctx.Reply(fmt.Sprintf("✅ Prefix diubah menjadi `%s`", p))
}
