package commands

import (
	"context"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"
)

// =================================================================
// GOOGLE DOCS (owner only) — buat catatan kuliah cepat ke Google Docs.
//
//	catat <judul> | <isi>   → buat dokumen baru berisi <isi>
//	catat <judul>           → buat dokumen kosong berjudul <judul>
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Catatan (Docs)",
		Category:    "Owner",
		Aliases:     []string{"catat", "docs", "catatan"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:catat|catatan|docs)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Buat catatan ke Google Docs",
		Execute:     ExecuteCatat,
	}).Use(OwnerOnlyMiddleware)
}

func docsService(ctx *ContextBot) (*docs.Service, error) {
	c, err := src.GoogleClient(ctx.Ctx)
	if err != nil {
		return nil, err
	}
	return docs.NewService(ctx.Ctx, option.WithHTTPClient(c))
}

func ExecuteCatat(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	if arg == "" {
		return ctx.Reply("⚠️ Format: `catat <judul> | <isi>`\nContoh: `catat Catatan Termodinamika | Hukum ke-0: ...`\n\n_Bisa juga reply sebuah pesan teks lalu ketik_ `catat <judul>` _untuk menyimpan isinya._")
	}

	title := arg
	body := ""
	if i := strings.Index(arg, "|"); i >= 0 {
		title = strings.TrimSpace(arg[:i])
		body = strings.TrimSpace(arg[i+1:])
	}
	// Bila tak ada isi eksplisit tapi me-reply teks → pakai teks itu sebagai isi.
	if body == "" {
		body = quotedText(ctx)
	}
	if title == "" {
		title = "Catatan"
	}

	_ = ctx.React("📝")
	srv, err := docsService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 30*time.Second)
	defer cancel()

	doc, err := srv.Documents.Create(&docs.Document{Title: title}).Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal membuat dokumen: " + err.Error())
	}
	if body != "" {
		_, err = srv.Documents.BatchUpdate(doc.DocumentId, &docs.BatchUpdateDocumentRequest{
			Requests: []*docs.Request{{
				InsertText: &docs.InsertTextRequest{
					Location: &docs.Location{Index: 1},
					Text:     body,
				},
			}},
		}).Context(c).Do()
		if err != nil {
			return ctx.Reply("⚠️ Dokumen dibuat tapi gagal mengisi teks: " + err.Error() +
				"\n🔗 https://docs.google.com/document/d/" + doc.DocumentId + "/edit")
		}
	}
	return ctx.Reply("✅ Catatan dibuat: *" + title + "*\n🔗 https://docs.google.com/document/d/" + doc.DocumentId + "/edit")
}
