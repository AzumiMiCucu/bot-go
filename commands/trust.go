package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// TRUST / UNTRUST — daftar user terpercaya per grup (kebal anti-bot).
// Admin & owner otomatis terpercaya.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Trust User",
		Category:    "Group",
		Aliases:     []string{"trust"},
		Pattern:     regexp.MustCompile(`(?i)^\s*trust\b.*`),
		Description: "Tandai user terpercaya (reply/tag) — kebal anti-bot",
		Execute:     ExecuteTrust,
	}).Use(GroupOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Untrust User",
		Category:    "Group",
		Aliases:     []string{"untrust"},
		Pattern:     regexp.MustCompile(`(?i)^\s*untrust\b.*`),
		Description: "Cabut status terpercaya user (reply/tag)",
		Execute:     ExecuteUntrust,
	}).Use(GroupOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "List Trust",
		Category:    "Group",
		Aliases:     []string{"listtrust", "trustlist"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(listtrust|trustlist)\s*$`),
		Description: "Lihat daftar user terpercaya di grup",
		Execute:     ExecuteListTrust,
	}).Use(GroupOnlyMiddleware)
}

// userPart mengambil bagian sebelum '@' dari sebuah JID string.
func userPart(jid string) string {
	return strings.Split(jid, "@")[0]
}

func ExecuteTrust(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa menandai trust.")
	}
	target := resolveTargetJID(ctx)
	if target == "" {
		return ctx.Reply("⚠️ Reply atau tag user yang ingin dipercaya.\nContoh: `trust @user`")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	up := userPart(target)
	src.DB.AddTrust(groupID, up)
	return ctx.Reply(fmt.Sprintf("✅ @%s sekarang *terpercaya* (kebal anti-bot).", up))
}

func ExecuteUntrust(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mencabut trust.")
	}
	target := resolveTargetJID(ctx)
	if target == "" {
		return ctx.Reply("⚠️ Reply atau tag user yang ingin dicabut trust-nya.")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	up := userPart(target)
	src.DB.RemoveTrust(groupID, up)
	return ctx.Reply(fmt.Sprintf("✅ @%s tidak lagi terpercaya.", up))
}

func ExecuteListTrust(ctx *ContextBot) error {
	groupID := ctx.ChatJID.ToNonAD().String()
	list := src.DB.GetTrustList(groupID)
	if len(list) == 0 {
		return ctx.Reply("📋 Belum ada user terpercaya di grup ini.")
	}
	var sb strings.Builder
	sb.WriteString("📋 *DAFTAR TRUSTED*\n\n")
	for i, u := range list {
		sb.WriteString(fmt.Sprintf("%d. @%s\n", i+1, userPart(u)))
	}
	return ctx.Reply(sb.String())
}
