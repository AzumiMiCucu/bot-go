package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// ANTILINK (SATU command, alias: antilink / linkset):
//   antilink on|off            → nyalakan / matikan (tanpa arg = toggle)
//   antilink add <pola>        → tambah link yang diblokir
//   antilink del <pola>        → hapus link dari daftar
//   antilink list              → lihat daftar
// Default (saat ON tanpa custom): blokir link grup WA (chat.whatsapp.com).
// Pesan pelanggar dihapus (bot harus admin) + peringatan.
// Admin / owner / trusted dikecualikan.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Anti-Link",
		Category:    "Group",
		Aliases:     []string{"antilink", "linkset"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:antilink|linkset)(?:\s+(.+))?\s*$`),
		Description: "Antilink grup: on/off + kelola daftar (add/del/list) — admin",
		Execute:     ExecuteAntilink,
	}).Use(GroupOnlyMiddleware)
}

func ExecuteAntilink(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur antilink.")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	args := strings.Fields(strings.TrimSpace(ctx.Args))

	action := ""
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}

	switch action {
	case "add", "tambah", "del", "delete", "hapus", "rm":
		return linksetModify(ctx, groupID, action, args)
	case "list", "daftar":
		return linksetList(ctx, groupID)
	case "on":
		return antilinkToggle(ctx, groupID, true)
	case "off":
		return antilinkToggle(ctx, groupID, false)
	case "help", "bantuan", "?":
		return ctx.Reply(antilinkHelp())
	case "status", "show":
		return antilinkStatus(ctx, groupID)
	default:
		// Tanpa arg → toggle status.
		return antilinkToggle(ctx, groupID, !src.DB.IsAntilinkOn(groupID))
	}
}

func antilinkToggle(ctx *ContextBot, groupID string, target bool) error {
	src.DB.SetAntilinkOn(groupID, target)
	if target {
		pats := src.DB.GetLinkPatterns(groupID)
		return ctx.Reply(fmt.Sprintf("🔗 *Antilink: AKTIF* ✅\nBlokir: *%s*\n_Kelola daftar: `antilink add/del/list`. Pastikan bot admin._", strings.Join(pats, ", ")))
	}
	return ctx.Reply("🔗 *Antilink: NONAKTIF* ❌")
}

func antilinkStatus(ctx *ContextBot, groupID string) error {
	status := "❌ OFF"
	if src.DB.IsAntilinkOn(groupID) {
		status = "✅ ON"
	}
	pats := src.DB.GetLinkPatterns(groupID)
	return ctx.Reply(fmt.Sprintf("🔗 *Antilink*\n\nStatus : %s\nBlokir : %s\n\n_Bantuan: `antilink help`._",
		status, strings.Join(pats, ", ")))
}

func antilinkHelp() string {
	return "📖 *Antilink*\n\n" +
		"`antilink on` / `antilink off` _(tanpa arg = toggle)_\n" +
		"`antilink add <pola>` — tambah link diblokir\n" +
		"`antilink del <pola>` — hapus dari daftar\n" +
		"`antilink list` — lihat daftar\n" +
		"`antilink status` — status singkat\n\n" +
		"_Default blokir:_ `chat.whatsapp.com`. Pesan pelanggar dihapus (bot harus admin). " +
		"Admin/trusted dikecualikan."
}

func linksetList(ctx *ContextBot, groupID string) error {
	pats := src.DB.GetLinkPatterns(groupID)
	var sb strings.Builder
	sb.WriteString("🔗 *DAFTAR LINK DIBLOKIR*\n\n")
	for i, p := range pats {
		sb.WriteString(fmt.Sprintf("%d. `%s`\n", i+1, p))
	}
	sb.WriteString("\n`antilink add <pola>` · `antilink del <pola>`")
	return ctx.Reply(sb.String())
}

func linksetModify(ctx *ContextBot, groupID, action string, args []string) error {
	if len(args) < 2 {
		return ctx.Reply("⚠️ Format: `antilink add <pola>` / `antilink del <pola>`\nContoh: `antilink add tiktok.com`")
	}
	pattern := strings.ToLower(strings.TrimSpace(args[1]))

	switch action {
	case "add", "tambah":
		src.DB.AddLinkPattern(groupID, pattern)
		return ctx.Reply(fmt.Sprintf("✅ Ditambahkan ke daftar blokir: `%s`", pattern))
	default: // del/delete/hapus/rm
		src.DB.RemoveLinkPattern(groupID, pattern)
		return ctx.Reply(fmt.Sprintf("✅ Dihapus dari daftar blokir: `%s`", pattern))
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
	if groupModExempt(ctx, groupID) {
		return false
	}

	su := ctx.SenderJID.ToNonAD().User
	revoke := ctx.Client.BuildRevoke(ctx.ChatJID, ctx.SenderJID, ctx.Msg.Info.ID)
	_, _ = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, revoke, src.AndroidExtra())
	_ = ctx.Reply(fmt.Sprintf("🚫 @%s, link tidak diperbolehkan di grup ini.", su))
	return true
}
