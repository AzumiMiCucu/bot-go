package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// =================================================================
// OWNER GRUP TOOLS — join via link & daftar grup.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Join Grup",
		Category:    "Owner",
		Aliases:     []string{"join"},
		Pattern:     regexp.MustCompile(`(?i)^join\s+(.+)$`),
		Description: "Membuat bot join ke grup via link undangan (owner)",
		Execute:     ExecuteJoinGroup,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "List Grup",
		Category:    "Owner",
		Aliases:     []string{"listgc", "listgroup",},
		Pattern:     regexp.MustCompile(`(?i)^\s*(listgrup|listgroup|grouplist)\s*$`),
		Description: "Menampilkan semua grup yang diikuti bot (owner)",
		Execute:     ExecuteListGroup,
	}).Use(OwnerOnlyMiddleware)
}

var inviteCodeRegex = regexp.MustCompile(`chat\.whatsapp\.com/([0-9A-Za-z]+)`)

func ExecuteJoinGroup(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	if arg == "" {
		return ctx.Reply("⚠️ Format: `join <link grup>`\nContoh: `join https://chat.whatsapp.com/xxxx`")
	}

	// Ambil kode undangan dari link (JoinGroupWithLink juga otomatis strip prefix).
	code := arg
	if m := inviteCodeRegex.FindStringSubmatch(arg); len(m) > 1 {
		code = m[1]
	}

	_ = ctx.React("⏳")
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	jid, err := ctx.Client.JoinGroupWithLink(c, code)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Gagal join grup: %v", err))
	}

	name := jid.String()
	if info, e := ctx.Client.GetGroupInfo(c, jid); e == nil && info.Name != "" {
		name = info.Name
	}
	return ctx.Reply(fmt.Sprintf("✅ Berhasil join grup: *%s*", name))
}

func ExecuteListGroup(ctx *ContextBot) error {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	groups, err := ctx.Client.GetJoinedGroups(c)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Gagal ambil daftar grup: %v", err))
	}
	if len(groups) == 0 {
		return ctx.Reply("📭 Bot belum tergabung di grup mana pun.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 *DAFTAR GRUP BOT* (%d)\n\n", len(groups)))
	for i, g := range groups {
		name := g.Name
		if name == "" {
			name = "(tanpa nama)"
		}
		sb.WriteString(fmt.Sprintf("*%d.* %s\n   `%s`\n   👥 %d member\n\n",
			i+1, name, g.JID.String(), len(g.Participants)))
	}
	return ctx.Reply(sb.String())
}
