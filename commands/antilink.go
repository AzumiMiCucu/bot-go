package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// ANTILINK (2 command terpisah):
//   antilink on|off        → nyalakan / matikan
//   linkset add <pola>     → tambah link yang diblokir
//   linkset del <pola>     → hapus link dari daftar
//   linkset list           → lihat daftar
// Default (saat ON tanpa custom): blokir link grup WA (chat.whatsapp.com).
// Pesan pelanggar dihapus (bot harus admin) + peringatan.
// Admin / owner / trusted dikecualikan.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Anti-Link",
		Category:    "Group",
		Aliases:     []string{"antilink"},
		Pattern:     regexp.MustCompile(`(?i)^\s*antilink(?:\s+(on|off))?\s*$`),
		Description: "Nyalakan/matikan antilink grup (admin)",
		Execute:     ExecuteAntilink,
	}).Use(GroupOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Link Set",
		Category:    "Group",
		Aliases:     []string{"linkset"},
		Pattern:     regexp.MustCompile(`(?i)^\s*linkset(?:\s+(.+))?\s*$`),
		Description: "Kelola daftar link yang diblokir: add/del/list (admin)",
		Execute:     ExecuteLinkset,
	}).Use(GroupOnlyMiddleware)
}

func ExecuteAntilink(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur antilink.")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	arg := strings.ToLower(strings.TrimSpace(ctx.Args))

	switch arg {
	case "on":
		src.DB.SetAntilinkOn(groupID, true)
		pats := src.DB.GetLinkPatterns(groupID)
		return ctx.Reply(fmt.Sprintf("🔗 *Antilink AKTIF*\nMemblokir: *%s*\n\n_Atur daftar: `linkset add/del/list`. Pastikan bot admin._", strings.Join(pats, ", ")))
	case "off":
		src.DB.SetAntilinkOn(groupID, false)
		return ctx.Reply("🔗 *Antilink NONAKTIF*.")
	default:
		status := "NONAKTIF"
		if src.DB.IsAntilinkOn(groupID) {
			status = "AKTIF"
		}
		return ctx.Reply(fmt.Sprintf("🔗 Antilink saat ini: *%s*\n\nGunakan: `antilink on` / `antilink off`", status))
	}
}

func ExecuteLinkset(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur daftar link.")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	args := strings.Fields(strings.TrimSpace(ctx.Args))

	if len(args) == 0 || strings.ToLower(args[0]) == "list" {
		pats := src.DB.GetLinkPatterns(groupID)
		var sb strings.Builder
		sb.WriteString("🔗 *DAFTAR LINK DIBLOKIR*\n\n")
		for i, p := range pats {
			sb.WriteString(fmt.Sprintf("%d. `%s`\n", i+1, p))
		}
		sb.WriteString("\n`linkset add <pola>` · `linkset del <pola>`")
		return ctx.Reply(sb.String())
	}

	action := strings.ToLower(args[0])
	if len(args) < 2 {
		return ctx.Reply("⚠️ Format: `linkset add <pola>` / `linkset del <pola>`\nContoh: `linkset add tiktok.com`")
	}
	pattern := strings.ToLower(strings.TrimSpace(args[1]))

	switch action {
	case "add", "tambah":
		src.DB.AddLinkPattern(groupID, pattern)
		return ctx.Reply(fmt.Sprintf("✅ Ditambahkan ke daftar blokir: `%s`", pattern))
	case "del", "delete", "hapus", "rm":
		src.DB.RemoveLinkPattern(groupID, pattern)
		return ctx.Reply(fmt.Sprintf("✅ Dihapus dari daftar blokir: `%s`", pattern))
	default:
		return ctx.Reply("⚠️ Aksi tidak dikenal. Pakai: `add`, `del`, atau `list`.")
	}
}

// HandleAntilink dipanggil dari handler. True bila pesan dikonsumsi (link terlarang).
func HandleAntilink(ctx *ContextBot) bool {
	if !ctx.IsGroup || ctx.IsOwner {
		return false
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	if !src.DB.IsAntilinkOn(groupID) {
		return false
	}

	text := strings.ToLower(strings.TrimSpace(ctx.TextMessage))
	if text == "" {
		return false
	}

	matched := false
	for _, p := range src.DB.GetLinkPatterns(groupID) {
		if p != "" && strings.Contains(text, p) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}

	// Kecualikan admin / trusted
	su := ctx.SenderJID.ToNonAD().User
	sl := ctx.SenderAlt.ToNonAD().User
	if (su != "" && src.DB.IsTrusted(groupID, su)) || (sl != "" && src.DB.IsTrusted(groupID, sl)) {
		return false
	}
	if isCachedAdmin(ctx, groupID) {
		return false
	}

	revoke := ctx.Client.BuildRevoke(ctx.ChatJID, ctx.SenderJID, ctx.Msg.Info.ID)
	_, _ = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, revoke, src.AndroidExtra())
	_ = ctx.Reply(fmt.Sprintf("🚫 @%s, link tidak diperbolehkan di grup ini.", su))
	return true
}
