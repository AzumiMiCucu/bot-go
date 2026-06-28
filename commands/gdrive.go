package commands

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// =================================================================
// GOOGLE DRIVE (owner only) — simpan media/file dari WhatsApp ke Drive.
//
//	(reply media/file) drive [nama]  → upload ke Drive
//	drive list                       → 10 file terbaru
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Drive",
		Category:    "Owner",
		Aliases:     []string{"drive", "gdrive"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:drive|gdrive)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Upload media/file (reply) ke Google Drive; `drive list`",
		Execute:     ExecuteDrive,
	}).Use(OwnerOnlyMiddleware)
}

func driveService(ctx *ContextBot) (*drive.Service, error) {
	c, err := src.GoogleClient(ctx.Ctx)
	if err != nil {
		return nil, err
	}
	return drive.NewService(ctx.Ctx, option.WithHTTPClient(c))
}

func ExecuteDrive(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	if strings.EqualFold(arg, "list") {
		return driveList(ctx)
	}

	data, mime, fname := extractAnyFile(ctx)
	if len(data) == 0 {
		return ctx.Reply("⚠️ Reply sebuah *gambar/video/audio/dokumen* lalu ketik `drive` untuk upload.\n`drive list` untuk lihat file terbaru.")
	}
	name := fname
	if arg != "" && !strings.EqualFold(arg, "upload") {
		name = arg
	}
	if name == "" {
		name = "wa-upload"
	}

	_ = ctx.React("📤")
	srv, err := driveService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 120*time.Second)
	defer cancel()
	created, err := srv.Files.Create(&drive.File{Name: name}).
		Media(bytes.NewReader(data)).Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal upload: " + err.Error())
	}
	_ = mime
	return ctx.Reply(fmt.Sprintf("✅ Terupload ke Drive: *%s*\n🔗 https://drive.google.com/file/d/%s/view", created.Name, created.Id))
}

func driveList(ctx *ContextBot) error {
	_ = ctx.React("📁")
	srv, err := driveService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 25*time.Second)
	defer cancel()
	res, err := srv.Files.List().OrderBy("modifiedTime desc").PageSize(10).
		Fields("files(id,name,mimeType,modifiedTime)").Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal ambil daftar: " + err.Error())
	}
	if len(res.Files) == 0 {
		return ctx.Reply("📭 Drive kosong / tak ada file.")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📁 *FILE DRIVE TERBARU* (%d)\n\n", len(res.Files)))
	for i, f := range res.Files {
		sb.WriteString(fmt.Sprintf("%d. *%s*\n   🔗 https://drive.google.com/file/d/%s/view\n", i+1, responTruncate(oneLine(f.Name), 50), f.Id))
	}
	return ctx.Reply(sb.String())
}

// extractAnyFile mengambil media/dokumen dari pesan yang di-reply ATAU pesan ini.
func extractAnyFile(ctx *ContextBot) (data []byte, mime, filename string) {
	candidates := []*waProto.Message{
		src.UnwrapMessage(ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()),
		src.UnwrapMessage(ctx.Msg.Message),
	}
	for _, m := range candidates {
		if m == nil {
			continue
		}
		switch {
		case m.GetDocumentMessage() != nil:
			d := m.GetDocumentMessage()
			if b, err := ctx.Client.Download(context.Background(), d); err == nil && len(b) > 0 {
				name := d.GetFileName()
				if name == "" {
					name = "document"
				}
				return b, d.GetMimetype(), name
			}
		case m.GetImageMessage() != nil:
			im := m.GetImageMessage()
			if b, err := ctx.Client.Download(context.Background(), im); err == nil && len(b) > 0 {
				return b, im.GetMimetype(), "image.jpg"
			}
		case m.GetVideoMessage() != nil:
			v := m.GetVideoMessage()
			if b, err := ctx.Client.Download(context.Background(), v); err == nil && len(b) > 0 {
				return b, v.GetMimetype(), "video.mp4"
			}
		case m.GetAudioMessage() != nil:
			a := m.GetAudioMessage()
			if b, err := ctx.Client.Download(context.Background(), a); err == nil && len(b) > 0 {
				return b, a.GetMimetype(), "audio.ogg"
			}
		}
	}
	return nil, "", ""
}
