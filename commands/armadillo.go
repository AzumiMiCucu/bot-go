package commands

import (
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// COMMAND: ARMADILLO — lihat pesan interop Meta (FBMessage) yang tertangkap.
//   armadillo            → status + ringkasan capture terakhir
//   armadillo verbose on|off → toggle log console
// Owner-only (jalur diagnostik protokol).
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Armadillo",
		Category:    "Owner",
		Aliases:     []string{"armadillo", "fbmsg"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:armadillo|fbmsg)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Lihat pesan interop Meta (Messenger/IG) yang tertangkap",
		Execute:     ExecuteArmadillo,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteArmadillo(ctx *ContextBot) error {
	arg := strings.ToLower(strings.TrimSpace(ctx.Args))

	if strings.HasPrefix(arg, "verbose") {
		switch {
		case strings.Contains(arg, "on"):
			src.FBSetVerbose(true)
			return ctx.Reply("🦔 Log console Armadillo: *ON*")
		case strings.Contains(arg, "off"):
			src.FBSetVerbose(false)
			return ctx.Reply("🦔 Log console Armadillo: *OFF*")
		}
	}

	caps, total := src.FBLastCaptures(5)
	if total == 0 {
		return ctx.Reply("🦔 *ARMADILLO*\n\nBelum ada pesan interop Meta (FBMessage) yang tertangkap.\n\n_Muncul saat user Messenger/Instagram mengirim ke nomor ini lewat interop Meta._")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🦔 *ARMADILLO — Interop Meta*\n\nTotal tertangkap: *%d*\nMenampilkan %d terakhir:\n", total, len(caps))
	for i, c := range caps {
		fmt.Fprintf(&b, "\n*%d.* %s\n› Dari: `%s`\n› Chat: `%s`\n› Tipe: `%s` (retry %d)\n› %s\n",
			i+1, c.Time.Format("02/01 15:04:05"), c.Sender, c.Chat, c.Kind, c.Retry, fbTruncate(c.Summary, 200))
	}
	return ctx.Reply(b.String())
}

func fbTruncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
