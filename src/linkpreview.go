package src

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"  // daftarkan dekoder GIF untuk image.Decode/DecodeConfig
	"image/jpeg"
	_ "image/png" // daftarkan dekoder PNG
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"

	xdraw "golang.org/x/image/draw"
)

// =================================================================
// LINK PREVIEW BESAR — kirim pesan TEKS biasa yang disertai preview
// gambar BESAR (link-preview PreviewType=IMAGE). Dipakai fitur seperti
// `lens`/`glens` agar menampilkan thumbnail hasil teratas berukuran
// besar sesuai dimensi aslinya, sementara daftar lengkap ada di teks.
// =================================================================

// shrinkJPEG menyusutkan gambar agar sisi terpanjang <= maxDim, lalu meng-encode
// JPEG. Bila gambar sudah cukup kecil, data asli dikembalikan apa adanya.
func shrinkJPEG(data []byte, maxDim int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return data, nil
	}
	nw, nh := w, h
	if w >= h {
		nw, nh = maxDim, h*maxDim/w
	} else {
		nh, nw = maxDim, w*maxDim/h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SendTextWithThumbnailID mengirim pesan TEKS biasa dengan PREVIEW GAMBAR BESAR.
//   - text       : isi pesan (mis. daftar lengkap hasil, bernomor).
//   - previewURL : URL yang dijadikan matched-text kartu preview (mis. sumber teratas).
//   - title/desc : judul & deskripsi kartu preview.
//   - thumb      : byte gambar preview (hasil TERATAS). Boleh kosong → kirim teks polos.
//
// Preview dirender BESAR (PreviewType=IMAGE) dengan dimensi sesuai gambar asli.
// Mengembalikan ID pesan terkirim agar bisa didaftarkan ke reply-router.
func SendTextWithThumbnailID(ctx *ContextBot, text, previewURL, title, desc string, thumb []byte) (string, error) {
	ext := &waProto.ExtendedTextMessage{
		Text: proto.String(text),
	}

	// ContextInfo: quote pesan pemicu agar tampil sebagai balasan (sama seperti builder lain).
	if ctx.Msg != nil {
		senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
		if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
			senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
		}
		ext.ContextInfo = &waProto.ContextInfo{
			StanzaID:      proto.String(ctx.Msg.Info.ID),
			Participant:   proto.String(senderStr),
			QuotedMessage: ctx.Msg.Message,
		}
	}

	// Pastikan thumbnail berupa JPEG (stiker WebP → JPEG); gambar hasil biasanya sudah JPEG/PNG.
	if jpg, err := ToJPEGForAPI(thumb); err == nil && len(jpg) > 0 {
		thumb = jpg
	}

	if len(thumb) > 0 {
		ext.PreviewType = waProto.ExtendedTextMessage_IMAGE.Enum()
		if previewURL != "" {
			ext.MatchedText = proto.String(previewURL)
		}
		if title != "" {
			ext.Title = proto.String(title)
		}
		if desc != "" {
			ext.Description = proto.String(desc)
		}
		// Dimensi asli → WA merender preview BESAR sesuai ukuran thumbnail hasil teratas.
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(thumb)); err == nil {
			ext.ThumbnailWidth = proto.Uint32(uint32(cfg.Width))
			ext.ThumbnailHeight = proto.Uint32(uint32(cfg.Height))
		}
		// Inline JPEGThumbnail (disusutkan agar pesan tak membengkak) untuk render cepat.
		inline := thumb
		if len(inline) > 96*1024 {
			if small, err := shrinkJPEG(inline, 800); err == nil {
				inline = small
			}
		}
		ext.JPEGThumbnail = inline
		// Hi-res via MediaLinkThumbnail (preview besar tetap tajam). Best-effort.
		if up, err := ctx.Client.Upload(context.Background(), thumb, whatsmeow.MediaLinkThumbnail); err == nil {
			ext.ThumbnailDirectPath = proto.String(up.DirectPath)
			ext.ThumbnailSHA256 = up.FileSHA256
			ext.ThumbnailEncSHA256 = up.FileEncSHA256
			ext.MediaKey = up.MediaKey
			ext.MediaKeyTimestamp = proto.Int64(time.Now().Unix())
		}
	}

	msg := &waProto.Message{ExtendedTextMessage: ext}
	msgID := GenerateAndroidMessageID()
	_, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msg, whatsmeow.SendRequestExtra{ID: msgID})
	return string(msgID), err
}
