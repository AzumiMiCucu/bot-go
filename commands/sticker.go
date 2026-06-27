package commands

import (
	"context"
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "Sticker Maker",
		Category:    "Tools",
		Aliases:     []string{"sticker", "stiker"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:sticker|stiker)(?:\s+(.*))?$`),
		Description: "Ubah Gambar/Video jadi stiker. Format: s [NamaPack|NamaAuthor]",
		Execute:     ExecuteSticker,
	})
}

func ExecuteSticker(ctx *ContextBot) error {
	// 1. Ekstrak media: gambar / video / STIKER / view-once (sekali lihat),
	//    baik dari reply maupun caption langsung.
	mediaData, filename, ok := extractStickerSource(ctx)
	if !ok || len(mediaData) == 0 {
		return ctx.Reply("⚠️ Kirim/Balas *gambar, video, stiker,* atau media *sekali lihat* dengan perintah *s*.\n\n📌 *Cara pakai:* `s NamaPack|NamaAuthor` _(opsional)_")
	}

	_ = ctx.React("⏳")

	// 2. Tentukan Nama Pack & Author dari Args (opsional: s PackKu|Namaku)
	packName := "Sticker by"
	authorName := ctx.PushName // Default: nama profil WA user
	if ctx.Args != "" {
		parts := strings.Split(ctx.Args, "|")
		packName = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			authorName = strings.TrimSpace(parts[1])
		}
	}

	// 3. Konversi via API ps.azumi.dev (terima file, balas buffer WebP langsung).
	webpBytes, ctype, err := src.MakeSticker(mediaData, filename, authorName, packName)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengonversi media menjadi stiker.")
	}
	// API tools membalas binary WebP; bila content-type JSON berarti error terselubung.
	if len(webpBytes) == 0 || strings.Contains(ctype, "application/json") {
		_ = ctx.React("❌")
		return ctx.Reply("❌ API stiker mengembalikan hasil tak valid.")
	}

	// 4. Upload Stiker WebP ke server WhatsApp
	respMedia, err := ctx.Client.Upload(context.Background(), webpBytes, whatsmeow.MediaImage)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengunggah stiker ke server WhatsApp.")
	}

	msgToSend := &waProto.Message{
		StickerMessage: &waProto.StickerMessage{
			URL:           &respMedia.URL,
			DirectPath:    &respMedia.DirectPath,
			MediaKey:      respMedia.MediaKey,
			FileEncSHA256: respMedia.FileEncSHA256,
			FileSHA256:    respMedia.FileSHA256,
			FileLength:    &respMedia.FileLength,
			Mimetype:      proto.String("image/webp"),
		},
	}

	// 5. Kirim Pesan Stiker
	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msgToSend, AndroidExtra())
	if err != nil {
		_ = ctx.React("❌")
		return err
	}
	_ = ctx.React("✅")
	return nil
}

// extractStickerSource mencari media yang bisa dijadikan stiker dari pesan ini
// atau pesan yang di-reply, termasuk yang dibungkus view-once (sekali lihat).
// Mengembalikan (buffer, namaFile, true) bila ditemukan.
func extractStickerSource(ctx *ContextBot) ([]byte, string, bool) {
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	// Utamakan pesan yang di-reply; bila tak ada, pakai pesan saat ini.
	for _, m := range []*waProto.Message{quoted, ctx.Msg.Message} {
		if data, name, ok := mediaFromMessage(ctx, m); ok {
			return data, name, true
		}
	}
	return nil, "", false
}

// mediaFromMessage mengunduh gambar/video/stiker dari satu pesan (membuka bungkus
// view-once lebih dulu). namaFile mengikuti tipe agar API mudah mengenali format.
func mediaFromMessage(ctx *ContextBot, m *waProto.Message) ([]byte, string, bool) {
	m = src.UnwrapMessage(m)
	if m == nil {
		return nil, "", false
	}
	switch {
	case m.GetImageMessage() != nil:
		if d, err := ctx.Client.Download(context.Background(), m.GetImageMessage()); err == nil && len(d) > 0 {
			return d, "image.jpg", true
		}
	case m.GetVideoMessage() != nil:
		if d, err := ctx.Client.Download(context.Background(), m.GetVideoMessage()); err == nil && len(d) > 0 {
			return d, "video.mp4", true
		}
	case m.GetStickerMessage() != nil:
		if d, err := ctx.Client.Download(context.Background(), m.GetStickerMessage()); err == nil && len(d) > 0 {
			return d, "sticker.webp", true
		}
	}
	return nil, "", false
}

// ==========================================
// HELPER: WEBP EXIF INJECTOR
// ==========================================
// Menggantikan fungsi 'addExif' (node-webpmux) pada JS dengan manipulasi RIFF mentah.
func AddExif(webpBytes []byte, packName, author string) []byte {
	jsonStr := fmt.Sprintf(`{"sticker-pack-id":"%d","sticker-pack-name":"%s","sticker-pack-publisher":"%s","emojis":["😀"]}`, time.Now().Unix(), packName, author)
	jsonBytes := []byte(jsonStr)

	// Format Little-Endian metadata dasar Exif untuk WhatsApp
	exifAttr := []byte{
		0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x41, 0x57, 0x07, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x16, 0x00, 0x00, 0x00,
	}
	binary.LittleEndian.PutUint32(exifAttr[14:18], uint32(len(jsonBytes)))
	exifData := append(exifAttr, jsonBytes...)

	exifChunkSize := uint32(len(exifData))
	exifChunk := make([]byte, 8+exifChunkSize)
	copy(exifChunk[0:4], []byte("EXIF"))
	binary.LittleEndian.PutUint32(exifChunk[4:8], exifChunkSize)
	copy(exifChunk[8:], exifData)

	// Pastikan ukuran genap (Padding RIFF)
	if exifChunkSize%2 != 0 {
		exifChunk = append(exifChunk, 0)
	}

	// Cek Header Validasi WebP
	if len(webpBytes) < 12 || string(webpBytes[0:4]) != "RIFF" || string(webpBytes[8:12]) != "WEBP" {
		return webpBytes
	}

	// Suntik EXIF ke akhir byte dan update total file size
	newWebp := append(webpBytes, exifChunk...)
	newSize := uint32(len(newWebp) - 8)
	binary.LittleEndian.PutUint32(newWebp[4:8], newSize)

	// Aktifkan bendera Exif (Bit-3) pada Header VP8X
	if string(newWebp[12:16]) == "VP8X" {
		newWebp[20] |= 0x08 
	}

	return newWebp
}