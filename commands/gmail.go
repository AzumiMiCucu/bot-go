package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// =================================================================
// GMAIL (read-only, owner only) — lihat & ringkas email terbaru.
//
//	email          → daftar email belum dibaca (7 hari, primary)
//	email ringkas  → ringkasan poin penting via AI
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Email (Gmail)",
		Category:    "Owner",
		Aliases:     []string{"email", "gmail"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:email|gmail)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Lihat/ringkas email Gmail terbaru",
		Execute:     ExecuteEmail,
	}).Use(OwnerOnlyMiddleware)
}

func gmailService(ctx *ContextBot) (*gmail.Service, error) {
	c, err := src.GoogleClient(ctx.Ctx)
	if err != nil {
		return nil, err
	}
	return gmail.NewService(ctx.Ctx, option.WithHTTPClient(c))
}

type emailItem struct {
	from, subject, snippet string
}

func ExecuteEmail(ctx *ContextBot) error {
	arg := strings.ToLower(strings.TrimSpace(ctx.Args))
	summarize := arg == "ringkas" || arg == "summary" || arg == "rangkum"

	_ = ctx.React("📧")
	srv, err := gmailService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 30*time.Second)
	defer cancel()

	list, err := srv.Users.Messages.List("me").Q("is:unread newer_than:7d category:primary").MaxResults(8).Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal ambil email: " + err.Error())
	}
	if len(list.Messages) == 0 {
		return ctx.Reply("📭 Tidak ada email belum dibaca (7 hari terakhir, kategori utama).")
	}

	var items []emailItem
	for _, m := range list.Messages {
		full, err := srv.Users.Messages.Get("me", m.Id).Format("metadata").
			MetadataHeaders("From", "Subject").Context(c).Do()
		if err != nil {
			continue
		}
		it := emailItem{snippet: full.Snippet}
		for _, h := range full.Payload.Headers {
			switch h.Name {
			case "From":
				it.from = h.Value
			case "Subject":
				it.subject = h.Value
			}
		}
		items = append(items, it)
	}

	if summarize {
		var b strings.Builder
		for i, e := range items {
			b.WriteString(fmt.Sprintf("%d. Dari: %s | Subjek: %s | Cuplikan: %s\n", i+1, e.from, e.subject, e.snippet))
		}
		text, _, err := src.GeminiChat(
			"Ringkas email-email berikut menjadi poin penting dan tindakan yang perlu diambil. Bahasa Indonesia, padat:\n\n"+b.String(),
			"Ringkas, jelas, dan to the point.", "")
		if err != nil {
			return ctx.Reply("❌ Gagal meringkas: " + err.Error())
		}
		return ctx.Reply("📧 *RINGKASAN EMAIL* (" + fmt.Sprintf("%d", len(items)) + " belum dibaca)\n\n" + text)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📧 *EMAIL BELUM DIBACA* (%d)\n\n", len(items)))
	for i, e := range items {
		sb.WriteString(fmt.Sprintf("%d. *%s*\n   👤 %s\n   _%s_\n\n",
			i+1, responTruncate(oneLine(e.subject), 60), responTruncate(oneLine(e.from), 40), responTruncate(oneLine(e.snippet), 90)))
	}
	sb.WriteString("_Ringkas semua dengan_ `email ringkas`")
	return ctx.Reply(sb.String())
}
