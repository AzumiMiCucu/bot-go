package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"
)

// =================================================================
// COMMAND: CEKNO — pra-saring bot/bisnis lewat USync, TANPA menunggu pesan.
//   cekno <nomor>        (atau reply/tag target)
// Menanya server WA langsung: terdaftar?, bot platform?, bisnis (verified)?,
// jumlah device. Untuk deteksi Baileys level-pesan tetap pakai `cekbot`.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Cek Nomor (USync)",
		Category:    "Tools",
		Aliases:     []string{"cekno", "ceknomor", "probe"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:cekno|ceknomor|probe)(?:\s+([\s\S]+))?\s*$`),
		Description: "Cek status nomor via USync (terdaftar/bot/bisnis/device) tanpa perlu pesan masuk",
		Execute:     ExecuteCekNo,
	})
}

func ExecuteCekNo(ctx *ContextBot) error {
	target := strings.TrimSpace(ctx.Args)

	// Fallback: ambil dari mention / reply.
	if target == "" {
		if ext := ctx.Msg.Message.GetExtendedTextMessage(); ext != nil {
			if ci := ext.GetContextInfo(); ci != nil {
				if m := ci.GetMentionedJID(); len(m) > 0 {
					target = m[0]
				} else if p := ci.GetParticipant(); p != "" {
					target = p
				}
			}
		}
	}
	if target == "" {
		return ctx.Reply("⚠️ Format: `cekno <nomor>`\nAtau reply/tag targetnya.\n\nContoh: `cekno 628123456789`")
	}

	_ = ctx.React("🔎")
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := src.ProbeNumber(c, ctx.Client, target)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal probe: " + err.Error())
	}
	_ = ctx.React("✅")

	icon := map[string]string{
		"bot-platform":   "🤖",
		"bisnis":         "🏢",
		"personal":       "👤",
		"tidak-terdaftar": "🚫",
	}[res.Verdict]
	if icon == "" {
		icon = "❓"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s *HASIL PROBE (USync)*\n\n", icon)
	fmt.Fprintf(&b, "› Input: `%s`\n", res.Input)
	if !res.JID.IsEmpty() {
		fmt.Fprintf(&b, "› JID: `%s`\n", res.JID.String())
	}
	fmt.Fprintf(&b, "› Terdaftar: %s\n", yesNo(res.Registered))
	fmt.Fprintf(&b, "› Vonis: *%s*\n", strings.ToUpper(res.Verdict))
	if res.Business {
		fmt.Fprintf(&b, "› Verified name: %s\n", res.VerifiedName)
	}
	if res.Registered {
		fmt.Fprintf(&b, "› Bot platform: %s\n", yesNo(res.IsBotJID))
		fmt.Fprintf(&b, "› Device tertaut: *%d* (HP utama: %s)\n", res.DeviceCount, yesNo(res.HasPrimary))
	}
	fmt.Fprintf(&b, "\n_%s_", res.Reason)
	if res.Verdict == "personal" {
		b.WriteString("\n\n💡 Untuk pastikan Baileys/bot library, pakai `cekbot` pada pesannya.")
	}
	return ctx.Reply(b.String())
}

func yesNo(v bool) string {
	if v {
		return "✅ Ya"
	}
	return "❌ Tidak"
}
