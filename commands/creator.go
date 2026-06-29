package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "Creator Contact",
		Category:    "General",
		Aliases:     []string{"owner", "developer"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:owner|developer)\s*$`),
		Description: "Menampilkan kontak pembuat bot & katalog creator",
		Execute:     ExecuteCreator,
	})
}

func ExecuteCreator(ctx *ContextBot) error {
	if ctx.Client == nil || ctx.Msg == nil {
		return fmt.Errorf("konteks tidak valid")
	}

	ownerNumber := src.AppConfig.OwnerNumber
	ownerName := src.AppConfig.OwnerName
	contextInfo := buildContextInfo(ctx)

	firstWord := ""
	if len(strings.Fields(ctx.TextMessage)) > 0 {
		firstWord = strings.ToLower(strings.Fields(ctx.TextMessage)[0])
	}

	if strings.Contains(firstWord, "owner") {
		return sendOwnerContact(ctx, ownerNumber, ownerName, contextInfo)
	}

	return sendCreatorCatalog(ctx, ownerNumber, ownerName, contextInfo)
}

func buildContextInfo(ctx *ContextBot) *waProto.ContextInfo {
	if ctx.Msg == nil {
		return nil
	}

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}

	return &waProto.ContextInfo{
		StanzaID:      proto.String(ctx.Msg.Info.ID),
		Participant:   proto.String(senderStr),
		QuotedMessage: ctx.Msg.Message,
	}
}

func sendOwnerContact(ctx *ContextBot, ownerNumber, ownerName string, contextInfo *waProto.ContextInfo) error {
	vcard := fmt.Sprintf(
		"BEGIN:VCARD\nVERSION:3.0\nFN:%s\nTEL;type=CELL;type=VOICE;waid=%s:+%s\nEND:VCARD",
		ownerName, ownerNumber, ownerNumber,
	)

	_, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, &waProto.Message{
		ContactMessage: &waProto.ContactMessage{
			DisplayName: proto.String(ownerName),
			Vcard:       proto.String(vcard),
			ContextInfo: contextInfo,
		},
	}, src.AndroidExtra())
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengirim kontak owner.")
	}
	_ = ctx.React("✅")
	return nil
}

func sendCreatorCatalog(ctx *ContextBot, ownerNumber, ownerName string, contextInfo *waProto.ContextInfo) error {
	go func() { _ = ctx.React("⏳") }()
	imgBytes, err := downloadImage("https://raw.githubusercontent.com/ZidniGz/dbdb/refs/heads/main/ResizedImage_2026-04-30_11-07-49_0538%5B1%5D.jpg")
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengambil gambar creator.")
	}

	uploaded, err := ctx.Client.Upload(context.Background(), imgBytes, whatsmeow.MediaImage)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengunggah gambar ke server WhatsApp.")
	}

	businessJID := fmt.Sprintf("%s@s.whatsapp.net", src.AppConfig.BotNumber)

	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, &waProto.Message{
		ProductMessage: &waProto.ProductMessage{
			Product: &waProto.ProductMessage_ProductSnapshot{
				ProductID:         proto.String("26635815629419642"),
				Title:             proto.String(ownerName),
				Description:       proto.String("Apa lu liat-liat"),
				CurrencyCode:      proto.String("IDR"),
				PriceAmount1000:   proto.Int64(0),
				ProductImageCount: proto.Uint32(1),
				ProductImage: &waProto.ImageMessage{
					URL:           proto.String(uploaded.URL),
					DirectPath:    proto.String(uploaded.DirectPath),
					MediaKey:      uploaded.MediaKey,
					Mimetype:      proto.String("image/jpeg"),
					FileEncSHA256: uploaded.FileEncSHA256,
					FileSHA256:    uploaded.FileSHA256,
					FileLength:    proto.Uint64(uint64(len(imgBytes))),
					Caption:       proto.String("Creator Bot AI Assistant"),
				},
			},
			BusinessOwnerJID: proto.String(businessJID),
			ContextInfo:      contextInfo,
		},
	}, src.AndroidExtra())
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Terjadi kesalahan saat mengirim katalog creator.")
	}

	_ = ctx.React("✅")
	return nil
}

func downloadImage(imageURL string) ([]byte, error) {
	resp, err := src.MediaClient.Get(imageURL)
	if err != nil {
		return nil, fmt.Errorf("gagal request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("gagal baca: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("gambar kosong")
	}
	return data, nil
}
