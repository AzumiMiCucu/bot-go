package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// COMMAND: PING (server) & IQ (raw node) — showcase seam SendIQ mentah.
//   • ping             → round-trip nyata ke server WA (IQ w:p/ping)
//   • iq get|set <ns> [tag] [to]  → kirim <iq> mentah, tampilkan XML balasan
// Owner-only: ini alat level-protokol, bisa memicu error stream kalau salah.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Ping Server",
		Category:    "Owner",
		Aliases:     []string{"ping", "pingwa"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(ping|pingwa)\s*$`),
		Description: "Ukur latency nyata ke server WhatsApp (IQ w:p/ping)",
		Execute:     ExecutePing,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Raw IQ",
		Category:    "Owner",
		Aliases:     []string{"iq", "rawiq"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:iq|rawiq)\s+([\s\S]+)$`),
		Description: "[Owner] Kirim <iq> mentah ke server: iq <get|set> <namespace> [tag] [to]",
		Execute:     ExecuteRawIQ,
	}).Use(OwnerOnlyMiddleware)
}

func ExecutePing(ctx *ContextBot) error {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dur, err := src.PingServer(c, ctx.Client)
	if err != nil {
		return ctx.Reply("❌ Ping gagal: " + err.Error())
	}
	return ctx.Reply(fmt.Sprintf("🏓 *Pong!*\nRound-trip ke server WhatsApp: *%d ms*", dur.Milliseconds()))
}

func ExecuteRawIQ(ctx *ContextBot) error {
	fields := strings.Fields(strings.TrimSpace(ctx.Args))
	if len(fields) < 2 {
		return ctx.Reply("⚠️ Format: `iq <get|set> <namespace> [tag] [to]`\n\nContoh:\n› `iq get w:p ping`\n› `iq get usync`\n› `iq get status`")
	}

	iqType := strings.ToLower(fields[0])
	if iqType != "get" && iqType != "set" {
		return ctx.Reply("⚠️ Tipe harus `get` atau `set`.")
	}
	namespace := fields[1]

	var children []waBinary.Node
	if len(fields) >= 3 && fields[2] != "-" {
		children = []waBinary.Node{{Tag: fields[2]}}
	}

	to := types.ServerJID
	if len(fields) >= 4 {
		if j, err := types.ParseJID(fields[3]); err == nil {
			to = j
		}
	}

	_ = ctx.React("⏳")
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := src.SendRawIQ(c, ctx.Client, namespace, iqType, to, children)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ IQ gagal: " + err.Error())
	}
	_ = ctx.React("✅")

	xml := resp.XMLString()
	if len(xml) > 3500 {
		xml = xml[:3500] + "\n… (dipotong)"
	}
	return ctx.Reply("📡 *Balasan server:*\n```" + xml + "```")
}
