package commands

import (
	"context"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
)

// =================================================================
// Helper command-layer untuk fitur media (sticker/removebg). Panggilan HTTP ke
// ps.azumi.dev ada di src/proxy.go (PostFileTool/MakeSticker/RemoveBg). Di sini
// hanya hal yang butuh whatsmeow: buka bungkus view-once, unduh & kirim media.
// =================================================================

// unwrapMessage membuka bungkus pesan view-once (sekali lihat) & documentWithCaption
// agar media di dalamnya bisa diakses. Mengembalikan pesan asli bila bukan bungkus.
func unwrapMessage(m *waProto.Message) *waProto.Message {
	if m == nil {
		return nil
	}
	switch {
	case m.GetViewOnceMessage().GetMessage() != nil:
		return m.GetViewOnceMessage().GetMessage()
	case m.GetViewOnceMessageV2().GetMessage() != nil:
		return m.GetViewOnceMessageV2().GetMessage()
	case m.GetViewOnceMessageV2Extension().GetMessage() != nil:
		return m.GetViewOnceMessageV2Extension().GetMessage()
	case m.GetDocumentWithCaptionMessage().GetMessage() != nil:
		return m.GetDocumentWithCaptionMessage().GetMessage()
	}
	return m
}

// downloadImageFrom mengambil buffer GAMBAR/STIKER dari sebuah pesan (langsung
// atau view-once). Mengembalikan (data, true) bila ada, atau (nil, false).
func downloadImageFrom(ctx *ContextBot, m *waProto.Message) ([]byte, bool) {
	m = unwrapMessage(m)
	if m == nil {
		return nil, false
	}
	if img := m.GetImageMessage(); img != nil {
		if data, err := ctx.Client.Download(context.Background(), img); err == nil && len(data) > 0 {
			return data, true
		}
	}
	if st := m.GetStickerMessage(); st != nil {
		if data, err := ctx.Client.Download(context.Background(), st); err == nil && len(data) > 0 {
			return data, true
		}
	}
	return nil, false
}

// sendImageBytes mengunggah & mengirim buffer gambar sebagai pesan gambar.
func sendImageBytes(ctx *ContextBot, data []byte, mimeType, caption string) error {
	up, err := ctx.Client.Upload(context.Background(), data, whatsmeow.MediaImage)
	if err != nil {
		return ctx.Reply("❌ Gagal mengunggah hasil ke server WhatsApp.")
	}
	length := uint64(len(data))
	msg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			URL:           &up.URL,
			DirectPath:    &up.DirectPath,
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    &length,
			Mimetype:      &mimeType,
		},
	}
	if caption != "" {
		msg.ImageMessage.Caption = &caption
	}
	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msg, AndroidExtra())
	return err
}
