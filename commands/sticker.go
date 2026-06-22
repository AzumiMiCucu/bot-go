package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

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
	var mediaData []byte
	var isVideo bool
	var err error

	// 1. Ekstrak Gambar/Video (Bisa dari Reply, bisa dari Caption langsung)
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()

	if quoted != nil {
		if quoted.GetImageMessage() != nil {
			mediaData, err = ctx.Client.Download(context.Background(), quoted.GetImageMessage())
		} else if quoted.GetVideoMessage() != nil {
			mediaData, err = ctx.Client.Download(context.Background(), quoted.GetVideoMessage())
			isVideo = true
		}
	} else {
		if ctx.Msg.Message.GetImageMessage() != nil {
			mediaData, err = ctx.Client.Download(context.Background(), ctx.Msg.Message.GetImageMessage())
		} else if ctx.Msg.Message.GetVideoMessage() != nil {
			mediaData, err = ctx.Client.Download(context.Background(), ctx.Msg.Message.GetVideoMessage())
			isVideo = true
		}
	}

	if len(mediaData) == 0 {
		return ctx.Reply("❌ Harap kirim gambar/video dengan caption *s* atau balas (reply) media dengan pesan *s*.")
	}

	ctx.Reply("⏳ Sedang memproses media menjadi stiker...")

	// 2. Persiapan payload ke sticker-api.openwa.dev
	base64Str := base64.StdEncoding.EncodeToString(mediaData)
	mimeType := "image/jpeg"
	endpoint := "prepareWebp"
	if isVideo {
		mimeType = "video/mp4"
		endpoint = "convertMp4BufferToWebpDataUrl"
	}

	// 3. Tentukan Nama Pack & Author dari Args (opsional: s PackKu|Namaku)
	packName := "Sticker by"
	authorName := ctx.PushName // Default menggunakan nama profil WA user
	if ctx.Args != "" {
		parts := strings.Split(ctx.Args, "|")
		packName = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			authorName = strings.TrimSpace(parts[1])
		}
	}

	payload := map[string]interface{}{
		"stickerMetadata": map[string]interface{}{
			"pack":   packName,
			"author": authorName,
			"keepScale": true,
		},
	}

	if isVideo {
		payload["file"] = fmt.Sprintf("data:%s;base64,%s", mimeType, base64Str)
	} else {
		payload["image"] = fmt.Sprintf("data:%s;base64,%s", mimeType, base64Str)
	}

	jsonPayload, _ := json.Marshal(payload)

	// 4. Hit API Konversi WebP
	resp, err := http.Post("https://sticker-api.openwa.dev/"+endpoint, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return ctx.Reply("❌ Gagal terhubung ke API stiker.")
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	// 5. Mengekstrak Base64 WebP dari Respons API
	var webpBase64 string
	if isVideo {
		// Endpoint Video merespons dengan String Base64 langsung ("data:image/webp;...")
		respStr := string(bodyBytes)
		respStr = strings.Trim(respStr, "\"")
		parts := strings.Split(respStr, "base64,")
		if len(parts) > 1 {
			webpBase64 = parts[1]
		} else {
			webpBase64 = respStr
		}
	} else {
		// Endpoint Gambar merespons dengan JSON
		var apiResp struct {
			WebpBase64 string `json:"webpBase64"`
		}
		json.Unmarshal(bodyBytes, &apiResp)
		webpBase64 = apiResp.WebpBase64
	}

	webpBytes, err := base64.StdEncoding.DecodeString(webpBase64)
	if err != nil || len(webpBytes) == 0 {
		return ctx.Reply("❌ Gagal mengonversi file menjadi format WebP.")
	}

	// 6. Injeksi Manual Metadata EXIF (Setara node-webpmux JS)
	finalWebp := AddExif(webpBytes, packName, authorName)

	// 7. Upload Stiker WebP ke server WhatsApp
	respMedia, err := ctx.Client.Upload(context.Background(), finalWebp, whatsmeow.MediaImage)
	if err != nil {
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

	// 8. Kirim Pesan Stiker
	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msgToSend, AndroidExtra())
	return err
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