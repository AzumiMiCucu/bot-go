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
	"google.golang.org/protobuf/proto"
)

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
