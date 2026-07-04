package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"bot-go/src"
)

// =================================================================
// MAINTENANCE — owner bisa menandai FITUR (command) apa pun ke mode
// maintenance. Saat sebuah fitur di-maintenance, pengguna non-owner
// yang memanggilnya akan dibalas "fitur sedang di maintenance" dan
// command TIDAK dijalankan (owner tetap bisa memakainya untuk uji).
//
// State disimpan persisten di bot_meta (key `maint:<Nama Command>`),
// jadi bertahan lintas-restart.
// =================================================================

// maintKey membentuk key bot_meta untuk status maintenance sebuah command.
func maintKey(name string) string { return "maint:" + strings.ToLower(name) }

// IsFeatureMaintenance melaporkan apakah command sedang dalam mode maintenance.
// Dipakai oleh handler sebelum mengeksekusi command.
func IsFeatureMaintenance(cmd *src.Command) bool {
	if cmd == nil || src.DB == nil {
		return false
	}
	return src.DB.GetMeta(maintKey(cmd.Name)) == "1"
}

// findCommandByAlias mencari command berdasar nama atau alias (case-insensitive).
func findCommandByAlias(q string) *src.Command {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	for _, c := range src.GetCommands() {
		if strings.EqualFold(c.Name, q) {
			return c
		}
		for _, a := range c.Aliases {
			if strings.EqualFold(a, q) {
				return c
			}
		}
	}
	return nil
}

func init() {
	RegisterCommand(Command{
		Name:        "Maintenance",
		Category:    "Owner",
		Aliases:     []string{"maintenance", "maint"},
		Pattern:     regexp.MustCompile(`(?is)^(?:maintenance|maint)(?:\s+([\s\S]+))?$`),
		Description: "[Owner] Set fitur ke mode maintenance (on/off/list)",
		Execute:     ExecuteMaintenance,
	})
}

func ExecuteMaintenance(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa mengatur maintenance.")
	}

	args := strings.Fields(strings.TrimSpace(ctx.Args))
	if len(args) == 0 {
		return ctx.Reply("🛠️ *MAINTENANCE*\n\n" +
			"• `maintenance on <fitur>` — set fitur ke maintenance\n" +
			"• `maintenance off <fitur>` — aktifkan kembali\n" +
			"• `maintenance list` — daftar fitur yang sedang maintenance\n\n" +
			"Nama fitur = alias command (mis. `lens`, `glens`, `hd`).\n" +
			"Contoh: `maintenance on lens`")
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "list":
		var on []string
		for _, c := range src.GetCommands() {
			if src.DB.GetMeta(maintKey(c.Name)) == "1" {
				on = append(on, c.Name)
			}
		}
		if len(on) == 0 {
			return ctx.Reply("✅ Tidak ada fitur yang sedang maintenance.")
		}
		sort.Strings(on)
		return ctx.Reply("🛠️ *Fitur maintenance:*\n• " + strings.Join(on, "\n• "))

	case "on", "off":
		if len(args) < 2 {
			return ctx.Reply("Sertakan nama fitur. Contoh: `maintenance " + sub + " lens`")
		}
		target := findCommandByAlias(args[1])
		if target == nil {
			return ctx.Reply("❌ Fitur `" + args[1] + "` tidak ditemukan. Cek nama/alias command-nya.")
		}
		if strings.EqualFold(target.Name, "Maintenance") {
			return ctx.Reply("❌ Tidak bisa mem-maintenance command maintenance itu sendiri.")
		}
		if sub == "on" {
			src.DB.SetMeta(maintKey(target.Name), "1")
			return ctx.Reply(fmt.Sprintf("🛠️ Fitur *%s* kini *MAINTENANCE*.\nPengguna non-owner akan diberi tahu saat memakainya.", target.Name))
		}
		src.DB.SetMeta(maintKey(target.Name), "0")
		return ctx.Reply(fmt.Sprintf("✅ Fitur *%s* kembali *AKTIF*.", target.Name))

	default:
		return ctx.Reply("Subperintah tidak dikenal. Gunakan: `on` / `off` / `list`.")
	}
}
