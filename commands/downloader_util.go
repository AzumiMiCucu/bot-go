package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// albumItem = satu media (gambar/video) yang sudah diunduh, siap dikirim sebagai
// bagian dari sebuah album WhatsApp.
type albumItem struct {
	Data    []byte
	IsVideo bool
}

// sendMediaAlbum mengirim banyak media sebagai SATU album (carousel WhatsApp):
// unggah semua media → kirim AlbumMessage induk → kirim tiap media sebagai anak
// yang menaut ke induk lewat MessageAssociation (MEDIA_ALBUM). Caption menempel
// di media pertama. Mengembalikan error bila media valid < 2 atau induk gagal,
// agar pemanggil bisa fallback ke pengiriman satu per satu.
func sendMediaAlbum(ctx *ContextBot, items []albumItem, caption string) error {
	type uploaded struct {
		res     whatsmeow.UploadResponse
		isVideo bool
	}
	var ups []uploaded
	imgCount, vidCount := 0, 0
	for _, it := range items {
		if len(it.Data) == 0 {
			continue
		}
		mt := whatsmeow.MediaImage
		if it.IsVideo {
			mt = whatsmeow.MediaVideo
		}
		upCtx, cancel := context.WithTimeout(ctx.Ctx, 90*time.Second)
		up, err := ctx.Client.Upload(upCtx, it.Data, mt)
		cancel()
		if err != nil {
			continue
		}
		ups = append(ups, uploaded{res: up, isVideo: it.IsVideo})
		if it.IsVideo {
			vidCount++
		} else {
			imgCount++
		}
	}
	if len(ups) < 2 {
		return fmt.Errorf("media album tak cukup (%d)", len(ups))
	}

	chat := ctx.ChatJID.ToNonAD()

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}
	quote := &waProto.ContextInfo{
		StanzaID:      proto.String(ctx.Msg.Info.ID),
		Participant:   proto.String(senderStr),
		QuotedMessage: ctx.Msg.Message,
	}

	album := &waProto.Message{AlbumMessage: &waE2E.AlbumMessage{
		ExpectedImageCount: proto.Uint32(uint32(imgCount)),
		ExpectedVideoCount: proto.Uint32(uint32(vidCount)),
		ContextInfo:        quote,
	}}
	resp, err := ctx.Client.SendMessage(ctx.Ctx, chat, album, src.AndroidExtra())
	if err != nil {
		return fmt.Errorf("kirim album induk: %w", err)
	}

	parentKey := &waProto.MessageKey{
		RemoteJID: proto.String(chat.String()),
		FromMe:    proto.Bool(true),
		ID:        proto.String(string(resp.ID)),
	}

	sent := 0
	for i, u := range ups {
		cptn := ""
		if i == 0 {
			cptn = caption
		}
		msg := buildAlbumChildMsg(u.res, u.isVideo, cptn)
		msg.MessageContextInfo = &waProto.MessageContextInfo{
			MessageAssociation: &waE2E.MessageAssociation{
				AssociationType:  waE2E.MessageAssociation_MEDIA_ALBUM.Enum(),
				ParentMessageKey: parentKey,
			},
		}
		if _, err := ctx.Client.SendMessage(ctx.Ctx, chat, msg, src.AndroidExtra()); err == nil {
			sent++
		}
	}
	if sent == 0 {
		return fmt.Errorf("semua anak album gagal terkirim")
	}
	return nil
}

// buildAlbumChildMsg menyusun pesan media (gambar/video) anak album dari hasil upload.
func buildAlbumChildMsg(up whatsmeow.UploadResponse, isVideo bool, caption string) *waProto.Message {
	if isVideo {
		return &waProto.Message{VideoMessage: &waProto.VideoMessage{
			Caption: proto.String(caption), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, Mimetype: proto.String("video/mp4"), FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	}
	return &waProto.Message{ImageMessage: &waProto.ImageMessage{
		Caption: proto.String(caption), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
		MediaKey: up.MediaKey, Mimetype: proto.String("image/jpeg"), FileEncSHA256: up.FileEncSHA256,
		FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}}
}

// =================================================================
// HELPER UNDUHAN FILE (dipakai bersama mediafire & mega). Resolve tautan via
// ps.azumi.dev → unduh → kirim sebagai DOKUMEN. File terlalu besar (> dlMaxUpload)
// → kirim tautan unduh langsung saja (hindari OOM / batas upload WhatsApp).
// =================================================================

const dlMaxUpload = 100 * 1024 * 1024 // 100 MB batas unggah ke WhatsApp

// sendFileDocument mengunduh file dari downloadURL lalu mengirimnya sebagai
// dokumen WhatsApp (mengutip pesan pemicu). File > dlMaxUpload → kirim tautan saja.
func sendFileDocument(ctx *ContextBot, downloadURL, filename, mimetype, caption string) error {
	data, err := downloadCapped(downloadURL, dlMaxUpload)
	if err != nil {
		// Terlalu besar / gagal → berikan tautan langsung agar tetap berguna.
		return fmt.Errorf("⚠️ %s\n\nFile mungkin terlalu besar untuk dikirim. Tautan unduh langsung:\n%s", err.Error(), downloadURL)
	}
	if mimetype == "" {
		mimetype = "application/octet-stream"
	}

	upCtx, cancel := context.WithTimeout(ctx.Ctx, 180*time.Second)
	defer cancel()

	up, err := ctx.Client.Upload(upCtx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("❌ Gagal mengunggah file ke WhatsApp.")
	}

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}

	msg := &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		Mimetype:      proto.String(mimetype),
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		FileName:      proto.String(filename),
		Title:         proto.String(filename),
		Caption:       proto.String(caption),
		ContextInfo: &waProto.ContextInfo{
			StanzaID:      proto.String(ctx.Msg.Info.ID),
			Participant:   proto.String(senderStr),
			QuotedMessage: ctx.Msg.Message,
		},
	}}
	_, err = ctx.Client.SendMessage(ctx.Ctx, ctx.ChatJID.ToNonAD(), msg, src.AndroidExtra())
	if err != nil {
		return fmt.Errorf("❌ Gagal mengirim dokumen.")
	}
	return nil
}

// downloadCapped mengunduh hingga maksimum `max` byte. Bila melebihi → error.
func downloadCapped(rawURL string, max int64) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Mobile Safari/537.36")

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gagal mengunduh")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status unduhan %d", resp.StatusCode)
	}
	if resp.ContentLength > max {
		return nil, fmt.Errorf("file terlalu besar (%d MB)", resp.ContentLength/1024/1024)
	}

	// Baca dengan batas keras (untuk server tanpa Content-Length).
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca file")
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file melebihi %d MB", max/1024/1024)
	}
	return data, nil
}
