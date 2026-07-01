package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// COMMAND: PREMIUM (khusus OWNER) — kelola user premium
//
//	premium add <target> [durasi]  → beri/perpanjang premium (default 30 hari)
//	premium del <target>           → cabut premium
//	premium list                   → daftar user premium + sisa waktu
//	premium help                   → bantuan
//
// target: reply pesan / tag @user / ketik nomor.
// durasi: 30d, 12h, 1w, 60m, 6mo, 1y, atau perm/permanen. Butuh SATUAN (angka
//
//	polos diabaikan agar tak bentrok dgn nomor telepon).
//
// Owner SELALU premium (tak perlu didaftarkan). Alias: premium / femboy / onlyfans.
// =================================================================

var reDurToken = regexp.MustCompile(`(?i)^\d+\s*(s|m|h|d|w|mo|y|detik|menit|jam|hari|minggu|bulan|tahun)$`)

func init() {
	RegisterCommand(Command{
		Name:        "Premium",
		Category:    "Owner",
		Aliases:     []string{"premium", "femboy", "onlyfans"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:premium|femboy|onlyfans)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Kelola user premium: premium add/del/list",
		Execute:     ExecutePremium,
	}).Use(OwnerOnlyMiddleware)
}

func ExecutePremium(ctx *ContextBot) error {
	args := strings.TrimSpace(ctx.Args)
	sub := ""
	rest := ""
	if args != "" {
		parts := strings.SplitN(args, " ", 2)
		sub = strings.ToLower(parts[0])
		if len(parts) > 1 {
			rest = strings.TrimSpace(parts[1])
		}
	}

	switch sub {
	case "add", "tambah", "give", "+":
		return premiumAdd(ctx, rest)
	case "del", "delete", "hapus", "rm", "cabut", "-":
		return premiumDel(ctx, rest)
	case "list", "daftar", "ls":
		return premiumList(ctx)
	case "", "help", "bantuan", "?":
		return ctx.Reply(premiumHelp())
	default:
		return ctx.Reply(premiumHelp())
	}
}

func premiumHelp() string {
	return "💎 *PREMIUM* (owner)\n\n" +
		"› `premium add <target> [durasi]` — beri/perpanjang\n" +
		"› `premium del <target>` — cabut\n" +
		"› `premium list` — daftar\n\n" +
		"*target*: reply / tag @user / nomor\n" +
		"*durasi*: 30d, 12h, 1w, 6mo, 1y, perm _(default 30d)_"
}

func premiumAdd(ctx *ContextBot, rest string) error {
	// Pisahkan token durasi (harus bersatuan) dari sisa (target).
	durStr := ""
	tokens := strings.Fields(rest)
	if len(tokens) > 0 {
		last := tokens[len(tokens)-1]
		if looksLikeDuration(last) {
			durStr = last
			tokens = tokens[:len(tokens)-1]
		}
	}
	targetText := strings.Join(tokens, " ")

	expiresAt, label, ok := src.ParsePremiumDuration(durStr)
	if !ok {
		return ctx.Reply("⚠️ Durasi tak valid. Contoh: `30d`, `12h`, `1w`, `6mo`, `perm`.")
	}

	targets := premiumResolveTargets(ctx, targetText)
	if len(targets) == 0 {
		return ctx.Reply("⚠️ Target tak ada.\nReply pesannya / tag @user / ketik nomor.")
	}

	var lines []string
	for _, t := range targets {
		nums := premiumNumbersFor(ctx, t)
		for _, n := range nums {
			src.DB.AddPremium(n, expiresAt)
		}
		lines = append(lines, "✅ +"+t.User)
	}
	return ctx.Reply(fmt.Sprintf("💎 *PREMIUM ditambahkan* (%s)\n\n%s", label, strings.Join(lines, "\n")))
}

func premiumDel(ctx *ContextBot, rest string) error {
	targets := premiumResolveTargets(ctx, strings.TrimSpace(rest))
	if len(targets) == 0 {
		return ctx.Reply("⚠️ Target tak ada.\nReply pesannya / tag @user / ketik nomor.")
	}
	var lines []string
	for _, t := range targets {
		removed := false
		for _, n := range premiumNumbersFor(ctx, t) {
			if src.DB.RemovePremium(n) {
				removed = true
			}
		}
		if removed {
			lines = append(lines, "🗑️ -"+t.User)
		} else {
			lines = append(lines, "➖ "+t.User+" (bukan premium)")
		}
	}
	return ctx.Reply("💎 *PREMIUM dicabut*\n\n" + strings.Join(lines, "\n"))
}

func premiumList(ctx *ContextBot) error {
	entries := src.DB.ListPremium()
	if len(entries) == 0 {
		return ctx.Reply("💎 Belum ada user premium.\n_(owner selalu premium)_")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "💎 *DAFTAR PREMIUM* (%d)\n\n", len(entries))
	for i, e := range entries {
		fmt.Fprintf(&b, "%d. +%s — %s\n", i+1, e.Number, src.FormatPremiumRemaining(e.ExpiresAt))
	}
	return ctx.Reply(strings.TrimRight(b.String(), "\n"))
}

// looksLikeDuration true hanya bila token BERSATUAN (mis. 30d) atau kata permanen —
// angka polos (mis. nomor telepon) TIDAK dianggap durasi.
func looksLikeDuration(tok string) bool {
	switch strings.ToLower(tok) {
	case "perm", "permanen", "permanent", "selamanya", "unli", "unlimited":
		return true
	}
	return reDurToken.MatchString(tok)
}

// premiumResolveTargets mengumpulkan target dari mention, pesan yang di-reply, dan
// nomor mentah pada teks.
func premiumResolveTargets(ctx *ContextBot, argText string) []types.JID {
	var out []types.JID
	seen := make(map[string]bool)
	add := func(j types.JID) {
		u := j.ToNonAD().User
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, j.ToNonAD())
		}
	}

	if ext := ctx.Msg.Message.GetExtendedTextMessage(); ext != nil {
		if ci := ext.GetContextInfo(); ci != nil {
			for _, m := range ci.GetMentionedJID() {
				if j, err := types.ParseJID(m); err == nil {
					add(j)
				}
			}
			if p := ci.GetParticipant(); p != "" {
				if j, err := types.ParseJID(p); err == nil {
					add(j)
				}
			}
		}
	}
	if len(out) == 0 && argText != "" {
		for _, m := range regexp.MustCompile(`\+?(\d{7,15})`).FindAllStringSubmatch(argText, -1) {
			if j, err := types.ParseJID(m[1] + "@s.whatsapp.net"); err == nil {
				add(j)
			}
		}
	}
	return out
}

// premiumNumbersFor mengumpulkan SEMUA bentuk nomor (asli + pasangan LID/PN) sebuah
// target, agar premium tetap cocok lintas addressing mode.
func premiumNumbersFor(ctx *ContextBot, target types.JID) []string {
	nums := map[string]bool{target.ToNonAD().User: true}
	c := context.Background()
	if lid, err := ctx.Client.Store.LIDs.GetLIDForPN(c, target.ToNonAD()); err == nil && !lid.IsEmpty() {
		nums[lid.ToNonAD().User] = true
	}
	if pn, err := ctx.Client.Store.LIDs.GetPNForLID(c, target.ToNonAD()); err == nil && !pn.IsEmpty() {
		nums[pn.ToNonAD().User] = true
	}
	out := make([]string, 0, len(nums))
	for n := range nums {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}
