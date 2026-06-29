package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "Instagram Downloader",
		Category:    "Downloader",
		Aliases:     []string{"igdl", "ig", "instagram"},
		Pattern:     regexp.MustCompile(`(?i)^(?:igdl|ig|instagram|igdownload)\s+(.+)`),
		Description: "Unduh media Instagram (reel/post/carousel) — kirim langsung",
		Execute:     ExecuteIgDownload,
	})
}

var reIgURL = regexp.MustCompile(`(?i)(https?://(?:www\.)?instagram\.com/[^\s]+)`)

func ExecuteIgDownload(ctx *ContextBot) error {
	m := reIgURL.FindStringSubmatch(ctx.TextMessage)
	if len(m) == 0 {
		return ctx.Reply("⚠️ Tidak menemukan URL Instagram yang valid.\n\nContoh:\n• `igdl https://www.instagram.com/reel/xxxx`\n• `igdl https://www.instagram.com/p/xxxx`")
	}
	link := m[1]
	go func() { _ = ctx.React("⏳") }()
	res, err := src.IgDownload(link)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengunduh: " + err.Error())
	}

	caption := "📸 *Instagram*"
	if res.Username != "" {
		caption += "\n👤 @" + res.Username
	}
	if res.Title != "" {
		caption += "\n📝 " + responTruncate(res.Title, 350)
	}

	// Banyak media (carousel) → kirim sebagai SATU album (bukan satu per satu).
	// Bila album gagal (mis. upload/parent error), fallback ke pengiriman terpisah.
	if len(res.Items) > 1 {
		if err := sendIgAlbum(ctx, res.Items, caption); err == nil {
			_ = ctx.React("✅")
			return nil
		}
	}

	// Satu media (atau fallback album): kirim per-item seperti biasa.
	sent := 0
	for i, item := range res.Items {
		cptn := ""
		if i == 0 {
			cptn = caption // caption hanya di media pertama
		}
		if len(res.Items) > 1 {
			prefix := fmt.Sprintf("📂 %d/%d", i+1, len(res.Items))
			if cptn == "" {
				cptn = prefix
			} else {
				cptn = prefix + "\n" + cptn
			}
		}
		if err := sendIgMedia(ctx, item, cptn); err == nil {
			sent++
		}
	}

	if sent == 0 {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengirim media Instagram.")
	}
	_ = ctx.React("✅")
	return nil
}

// igUpload menampung satu media yang sudah diunggah ke server WA.
type igUpload struct {
	res     whatsmeow.UploadResponse
	isVideo bool
}

// sendIgAlbum mengunggah SEMUA media lalu mengirimnya sebagai satu pesan ALBUM
// (carousel WhatsApp). Caranya: kirim pesan AlbumMessage induk (berisi jumlah
// gambar/video yang diharapkan), lalu tiap media dikirim sebagai anak yang
// menautkan diri ke induk lewat MessageContextInfo.MessageAssociation (MEDIA_ALBUM).
// Mengembalikan error bila induk gagal / tak ada media yang berhasil dikirim →
// pemanggil melakukan fallback ke pengiriman satu per satu.
func sendIgAlbum(ctx *ContextBot, items []src.IgMediaItem, caption string) error {
	// ── Unduh + unggah semua media dulu ──
	var ups []igUpload
	imgCount, vidCount := 0, 0
	for _, item := range items {
		data, err := downloadIgBytes(item.URL)
		if err != nil || len(data) == 0 {
			continue
		}
		mt := whatsmeow.MediaImage
		if item.IsVideo {
			mt = whatsmeow.MediaVideo
		}
		upCtx, cancel := context.WithTimeout(ctx.Ctx, 90*time.Second)
		up, err := ctx.Client.Upload(upCtx, data, mt)
		cancel()
		if err != nil {
			continue
		}
		ups = append(ups, igUpload{res: up, isVideo: item.IsVideo})
		if item.IsVideo {
			vidCount++
		} else {
			imgCount++
		}
	}
	if len(ups) < 2 {
		return fmt.Errorf("media album tak cukup (%d) — fallback", len(ups))
	}

	chat := ctx.ChatJID.ToNonAD()

	// ── Kutip pesan pemicu pada induk album ──
	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}
	quote := &waProto.ContextInfo{
		StanzaID:      proto.String(ctx.Msg.Info.ID),
		Participant:   proto.String(senderStr),
		QuotedMessage: ctx.Msg.Message,
	}

	// ── Kirim AlbumMessage induk ──
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

	// ── Kirim tiap media sebagai anak album ──
	sent := 0
	for i, u := range ups {
		cptn := ""
		if i == 0 {
			cptn = caption // caption album menempel di media pertama
		}
		msg := buildIgMediaMsg(u.res, u.isVideo, cptn)
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

// buildIgMediaMsg menyusun pesan media (gambar/video) dari hasil upload. ContextInfo
// album (MessageAssociation) ditambahkan terpisah oleh pemanggil.
func buildIgMediaMsg(up whatsmeow.UploadResponse, isVideo bool, caption string) *waProto.Message {
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

// sendIgMedia mengunduh satu media dari CDN lalu mengirimnya (video/gambar)
// sebagai balasan yang mengutip pesan pemicu.
func sendIgMedia(ctx *ContextBot, item src.IgMediaItem, caption string) error {
	data, err := downloadIgBytes(item.URL)
	if err != nil || len(data) == 0 {
		return fmt.Errorf("unduh gagal: %v", err)
	}

	upCtx, cancel := context.WithTimeout(ctx.Ctx, 90*time.Second)
	defer cancel()

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}
	quote := &waProto.ContextInfo{
		StanzaID:      proto.String(ctx.Msg.Info.ID),
		Participant:   proto.String(senderStr),
		QuotedMessage: ctx.Msg.Message,
	}

	var msg *waProto.Message
	if item.IsVideo {
		up, err := ctx.Client.Upload(upCtx, data, whatsmeow.MediaVideo)
		if err != nil {
			return fmt.Errorf("upload video: %w", err)
		}
		msg = &waProto.Message{VideoMessage: &waProto.VideoMessage{
			Caption: proto.String(caption), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, Mimetype: proto.String("video/mp4"), FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength), ContextInfo: quote,
		}}
	} else {
		up, err := ctx.Client.Upload(upCtx, data, whatsmeow.MediaImage)
		if err != nil {
			return fmt.Errorf("upload gambar: %w", err)
		}
		msg = &waProto.Message{ImageMessage: &waProto.ImageMessage{
			Caption: proto.String(caption), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, Mimetype: proto.String("image/jpeg"), FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength), ContextInfo: quote,
		}}
	}

	_, err = ctx.Client.SendMessage(ctx.Ctx, ctx.ChatJID.ToNonAD(), msg, src.AndroidExtra())
	return err
}

// downloadIgBytes mengunduh file dari CDN dengan User-Agent browser.
func downloadIgBytes(rawURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Mobile Safari/537.36")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status unduhan %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
