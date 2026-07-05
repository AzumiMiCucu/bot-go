package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// TRUST / UNTRUST — daftar user terpercaya per grup (kebal anti-bot).
// Admin & owner otomatis terpercaya.
// =================================================================

func init() {
	// Satu handler untuk trust & untrust; arah ditentukan via if/switch di dalam.
	RegisterCommand(Command{
		Name:        "Trust / Untrust",
		Category:    "Group",
		Aliases:     []string{"trust", "untrust"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(un)?trust\b.*`),
		Description: "Tandai / cabut user terpercaya (reply/tag) — kebal anti-bot",
		Execute:     ExecuteTrustToggle,
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

// resolveTargetJID mengambil JID tujuan dari mention (@user) atau pesan yang di-reply.
func resolveTargetJID(ctx *ContextBot) string {
	ext := ctx.Msg.Message.GetExtendedTextMessage()
	if ext == nil {
		return ""
	}
	ctxInfo := ext.GetContextInfo()
	if ctxInfo == nil {
		return ""
	}

	// 1. Mention (@user)
	for _, m := range ctxInfo.GetMentionedJID() {
		if parsed, err := types.ParseJID(m); err == nil {
			return parsed.String()
		}
	}

	// 2. Reply → participant pesan yang di-quote
	if participant := ctxInfo.GetParticipant(); participant != "" {
		if parsed, err := types.ParseJID(participant); err == nil {
			return parsed.String()
		}
	}

	return ""
}

// ExecuteTrustToggle menangani trust DAN untrust dalam satu handler.
func ExecuteTrustToggle(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur trust.")
	}

	// Deteksi perintah: untrust vs trust
	isUntrust := strings.HasPrefix(strings.ToLower(strings.TrimSpace(ctx.TextMessage)), "untrust")

	target := resolveTargetJID(ctx)
	if target == "" {
		return ctx.Reply("⚠️ Reply atau tag user-nya.\nContoh: `trust @user` / `untrust @user`")
	}

	groupID := ctx.ChatJID.ToNonAD().String()
	up := userPart(target)

	switch {
	case isUntrust:
		src.DB.RemoveTrust(groupID, up)
		return ctx.Reply(fmt.Sprintf("✅ @%s tidak lagi terpercaya.", up))
	default:
		src.DB.AddTrust(groupID, up)
		return ctx.Reply(fmt.Sprintf("✅ @%s sekarang *terpercaya* (kebal anti-bot).", up))
	}
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
