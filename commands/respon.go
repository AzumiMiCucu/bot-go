package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// AUTO-RESPON (CRUD lengkap): bot membalas otomatis suatu pesan berdasarkan
// PEMICU yang di-set owner. Pemicu bisa TEKS (keyword) atau STIKER (dicocokkan
// via hash). Balasan bisa TEKS atau MEDIA (gambar/video/audio/stiker). Disimpan
// di respon.db (lihat src/respondb.go).
//
// SATU command "respon" dengan SUB-PERINTAH (bukan banyak command terpisah):
//   respon add <keyword> [| teks]      → tambah pemicu teks (reply media → balasan media)
//   respon edit <keyword> [| teks]     → ubah (menimpa)
//   respon addstiker [label] [| teks]  → pemicu STIKER (reply stiker pemicunya)
//   respon hapus <keyword|label>       → hapus
//   respon list                        → daftar semua
//   respon get <keyword>               → detail
//   respon help                        → bantuan
//
// Auto-respon AKTIF hanya saat bot TIDAK dalam mode self (lihat HandleAutoRespon).
// =================================================================

func init() {
	// SATU command terdaftar dengan SUB-PERINTAH (add/edit/hapus/list/get).
	RegisterCommand(Command{
		Name:        "Respon",
		Category:    "Owner",
		Aliases:     []string{"respon", "response"},
		Pattern:     regexp.MustCompile(`(?i)^\s*respon(?:se)?(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Kelola auto-respon: respon add/edit/hapus/list/get",
		Execute:     ExecuteRespon,
	}).Use(OwnerOnlyMiddleware)
}

// ExecuteRespon mengurai sub-perintah dari `respon ...`.
func ExecuteRespon(ctx *ContextBot) error {
	args := strings.TrimSpace(ctx.Args)
	sub := ""
	rest := ""
	if args != "" {
		parts := strings.SplitN(args, " ", 2)
		sub = strings.ToLower(parts[0])
		if len(parts) > 1 {
			rest = strings.TrimSpace(parts[1])
		}
	}

	switch sub {
	case "add", "tambah":
		return responAdd(ctx, rest, false)
	case "edit", "ubah":
		return responAdd(ctx, rest, true)
	case "addstiker", "addsticker", "stiker", "sticker":
		// Trigger STIKER: balas/​reply sebuah stiker lalu set balasannya.
		return responAddSticker(ctx, rest)
	case "del", "delete", "hapus", "rm":
		return responDel(ctx, rest)
	case "list", "daftar", "ls":
		return responList(ctx)
	case "get", "detail", "lihat", "info":
		return responGet(ctx, rest)
	case "", "help", "bantuan", "?":
		return ctx.Reply(responHelp())
	default:
		// `respon <keyword>` tanpa sub → anggap minta detail keyword tsb.
		return responGet(ctx, args)
	}
}

// responAddSticker membuat aturan respon yang DIPICU oleh sebuah STIKER. Owner
// me-reply stiker pemicu, lalu menentukan balasan:
//   - teks      → `respon addstiker [label] | <teks balasan>`
//   - media     → kirim gambar/video/audio (caption `respon addstiker [label]`) sambil reply stiker
//
// `label` opsional dipakai untuk menampilkan & menghapus (`respon hapus <label>`).
func responAddSticker(ctx *ContextBot, arg string) error {
	stk := quotedSticker(ctx)
	if stk == nil {
		return ctx.Reply("⚠️ Reply (balas) sebuah *stiker* lalu ketik:\n`respon addstiker [label] | <balasan>`\n\n_Atau kirim media (gambar/video) dengan caption `respon addstiker [label]` sambil mereply stikernya._")
	}
	if len(stk.GetFileSHA256()) == 0 {
		return ctx.Reply("⚠️ Stiker tidak valid (hash kosong).")
	}

	label := strings.TrimSpace(arg)
	respText := ""
	if i := strings.Index(arg, "|"); i >= 0 {
		label = strings.TrimSpace(arg[:i])
		respText = strings.TrimSpace(arg[i+1:])
	}

	key := stickerKey(stk.GetFileSHA256())
	if label == "" {
		// Label default dari potongan hash agar tetap bisa dihapus & dikenali.
		label = "stiker-" + strings.TrimPrefix(key, "stk:")[:6]
	}

	// Balasan MEDIA dari pesan ini sendiri (bukan dari yang di-reply = itu pemicu).
	rtype, data, mime, _ := extractCurrentMedia(ctx)
	if rtype != "" && len(data) > 0 {
		if err := src.AddResponFull(key, src.TriggerSticker, label, src.MatchExact, rtype, respText, mime, data, ctx.User); err != nil {
			return ctx.Reply("❌ Gagal menyimpan respon: " + err.Error())
		}
		return ctx.Reply(responStickerSavedMsg(label, rtype))
	}

	if respText == "" {
		return ctx.Reply("⚠️ Belum ada balasan.\n\nGunakan: `respon addstiker [label] | <teks>` atau lampirkan media dengan caption `respon addstiker [label]`.")
	}
	if err := src.AddResponFull(key, src.TriggerSticker, label, src.MatchExact, src.ResponText, respText, "", nil, ctx.User); err != nil {
		return ctx.Reply("❌ Gagal menyimpan respon: " + err.Error())
	}
	return ctx.Reply(responStickerSavedMsg(label, src.ResponText))
}

func responStickerSavedMsg(label, rtype string) string {
	return fmt.Sprintf("✅ Respon *stiker* tersimpan.\n\n🏷️ Label : `%s`\n🧩 Balasan: %s %s\n🎯 Pemicu : kirim stiker yang sama\n\n_Hapus dengan_ `respon hapus %s`",
		label, responTypeIcon(rtype), rtype, label)
}

// responAdd menambah / mengubah aturan respon. `arg` = "<keyword> [| teks balasan]".
// Bila ada pesan yang di-reply berisi media → balasan media (caption = teks bila ada).
// Bila reply berisi teks → balasan teks itu. Bila tak ada reply → wajib pakai "| teks".
// Flag "--contains" (di mana saja pada keyword) → pencocokan "mengandung".
func responAdd(ctx *ContextBot, arg string, isEdit bool) error {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ctx.Reply(responHelp())
	}

	// Pisahkan keyword | teks balasan eksplisit.
	keyword := arg
	explicitText := ""
	if i := strings.Index(arg, "|"); i >= 0 {
		keyword = strings.TrimSpace(arg[:i])
		explicitText = strings.TrimSpace(arg[i+1:])
	}

	// Deteksi flag pencocokan.
	matchType := src.MatchExact
	if m := regexp.MustCompile(`(?i)\s*--contains\b`); m.MatchString(keyword) {
		matchType = src.MatchContains
		keyword = strings.TrimSpace(m.ReplaceAllString(keyword, ""))
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return ctx.Reply("⚠️ Keyword kosong. Contoh: `respon add halo | Halo juga!`")
	}

	_, exists := src.GetRespon(keyword)
	if isEdit && !exists {
		return ctx.Reply(fmt.Sprintf("⚠️ Respon `%s` belum ada. Pakai `respon add` untuk membuat baru.", keyword))
	}

	// Coba ambil media dari pesan yang di-reply (atau pesan ini sendiri).
	rtype, data, mime, _ := extractResponMedia(ctx)
	if rtype != "" && len(data) > 0 {
		caption := explicitText
		if caption == "" {
			// Bila reply tak punya teks tambahan, pakai caption asli media yang di-reply.
			caption = quotedCaption(ctx)
		}
		if err := src.AddRespon(keyword, matchType, rtype, caption, mime, data, ctx.User); err != nil {
			return ctx.Reply("❌ Gagal menyimpan respon: " + err.Error())
		}
		return ctx.Reply(responSavedMsg(keyword, rtype, matchType, isEdit))
	}

	// Tidak ada media → balasan TEKS. Sumber teks: eksplisit "| teks", atau teks
	// pesan yang di-reply.
	text := explicitText
	if text == "" {
		text = quotedText(ctx)
	}
	if text == "" {
		return ctx.Reply("⚠️ Tidak ada isi balasan.\n\nGunakan salah satu:\n• `respon add <keyword> | <teks balasan>`\n• reply sebuah pesan/media lalu ketik `respon add <keyword>`")
	}
	if err := src.AddRespon(keyword, matchType, src.ResponText, text, "", nil, ctx.User); err != nil {
		return ctx.Reply("❌ Gagal menyimpan respon: " + err.Error())
	}
	return ctx.Reply(responSavedMsg(keyword, src.ResponText, matchType, isEdit))
}

func responSavedMsg(keyword, rtype, matchType string, isEdit bool) string {
	verb := "Ditambahkan"
	if isEdit {
		verb = "Diperbarui"
	}
	mt := "sama persis"
	if matchType == src.MatchContains {
		mt = "mengandung"
	}
	return fmt.Sprintf("✅ Respon %s.\n\n🔑 Keyword : `%s`\n🧩 Tipe    : %s\n🎯 Cocok   : %s\n\n_Akan dibalas otomatis saat bot tidak mode self._",
		strings.ToLower(verb), keyword, rtype, mt)
}

func responDel(ctx *ContextBot, arg string) error {
	keyword := strings.ToLower(strings.TrimSpace(arg))
	if keyword == "" {
		return ctx.Reply("⚠️ Format: `delrespon <keyword>`")
	}
	ok, err := src.DelRespon(keyword)
	if err != nil {
		return ctx.Reply("❌ Gagal menghapus: " + err.Error())
	}
	if !ok {
		return ctx.Reply(fmt.Sprintf("⚠️ Respon `%s` tidak ditemukan.", keyword))
	}
	return ctx.Reply(fmt.Sprintf("🗑️ Respon `%s` dihapus.", keyword))
}

func responList(ctx *ContextBot) error {
	list := src.ListRespon()
	if len(list) == 0 {
		return ctx.Reply("📭 Belum ada auto-respon. Tambah dengan `addrespon <keyword> | <balasan>`.")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 *DAFTAR AUTO-RESPON* (%d)\n\n", len(list)))
	for i, r := range list {
		icon := responTypeIcon(r.Type)
		mt := ""
		if r.MatchType == src.MatchContains {
			mt = " ~"
		}
		preview := ""
		if r.Type == src.ResponText {
			preview = " — " + responTruncate(oneLine(r.Text), 30)
		} else if r.Text != "" {
			preview = " — 📝 " + responTruncate(oneLine(r.Text), 24)
		}
		// Trigger stiker tampil pakai LABEL (keyword-nya berupa hash).
		name := r.Keyword
		if r.TriggerType == src.TriggerSticker {
			name = "🔖 " + r.Label + " (stiker)"
		}
		sb.WriteString(fmt.Sprintf("%d. %s `%s`%s%s\n", i+1, icon, name, mt, preview))
	}
	sb.WriteString("\n_`~` = cocok mengandung. Detail: `respon get <keyword>`._")
	return ctx.Reply(sb.String())
}

func responGet(ctx *ContextBot, arg string) error {
	keyword := strings.ToLower(strings.TrimSpace(arg))
	if keyword == "" {
		return ctx.Reply("⚠️ Format: `respon get <keyword>`")
	}
	r, ok := src.GetRespon(keyword)
	if !ok {
		return ctx.Reply(fmt.Sprintf("⚠️ Respon `%s` tidak ditemukan.", keyword))
	}
	mt := "sama persis"
	if r.MatchType == src.MatchContains {
		mt = "mengandung"
	}
	var sb strings.Builder
	sb.WriteString("🔎 *DETAIL RESPON*\n\n")
	sb.WriteString(fmt.Sprintf("🔑 Keyword : `%s`\n", r.Keyword))
	sb.WriteString(fmt.Sprintf("🧩 Tipe    : %s %s\n", responTypeIcon(r.Type), r.Type))
	sb.WriteString(fmt.Sprintf("🎯 Cocok   : %s\n", mt))
	if r.Type == src.ResponText {
		sb.WriteString(fmt.Sprintf("💬 Balasan : %s\n", responTruncate(r.Text, 300)))
	} else {
		sb.WriteString("📎 Media   : tersimpan\n")
		if r.Text != "" {
			sb.WriteString(fmt.Sprintf("📝 Caption : %s\n", responTruncate(r.Text, 200)))
		}
	}
	if r.CreatedBy != "" {
		sb.WriteString(fmt.Sprintf("👤 Dibuat  : @%s\n", r.CreatedBy))
	}
	return ctx.Reply(sb.String())
}

func responHelp() string {
	return "📖 *AUTO-RESPON* (sub-perintah `respon ...`)\n\n" +
		"`respon add <keyword> | <teks>` — balasan teks\n" +
		"reply media + `respon add <keyword>` — balasan media\n" +
		"`respon edit <keyword> ...` — ubah respon\n" +
		"`respon hapus <keyword|label>` — hapus\n" +
		"`respon list` — daftar semua\n" +
		"`respon get <keyword>` — detail\n\n" +
		"🔖 *Trigger stiker* (kirim stiker → bot balas):\n" +
		"reply stiker + `respon addstiker [label] | <balasan teks>`\n" +
		"_atau_ kirim gambar/video (caption `respon addstiker [label]`) sambil reply stikernya.\n\n" +
		"_Opsi:_ tambahkan `--contains` agar cocok bila pesan *mengandung* keyword " +
		"(default: sama persis).\n" +
		"_Catatan:_ auto-respon hanya jalan saat grup *tidak* mode self."
}

// ================= AUTO-RESPONSE RUNTIME (dipanggil dari handler) =================

// HandleAutoRespon mencari respon yang cocok untuk pesan masuk lalu mengirimnya.
// Mengembalikan true bila sebuah respon dikirim (pesan dikonsumsi). NONAKTIF saat
// grup dalam mode self (sesuai permintaan: respon berlaku bila bot tidak self).
func HandleAutoRespon(ctx *ContextBot) bool {
	if src.ResponCount() == 0 {
		return false
	}
	// Mode self (per-grup) → auto-respon dimatikan.
	if ctx.IsGroup && src.DB.IsGroupSelf(ctx.ChatJID.ToNonAD().String()) {
		return false
	}

	// 1) Trigger STIKER: pesan masuk berupa stiker → cocokkan via hash.
	if stk := src.UnwrapMessage(ctx.Msg.Message).GetStickerMessage(); stk != nil {
		if len(stk.GetFileSHA256()) == 0 {
			return false
		}
		r, ok := src.MatchResponSticker(stickerKey(stk.GetFileSHA256()))
		if !ok {
			return false
		}
		if err := sendRespon(ctx, r); err != nil {
			ctx.Print("[RESPON] gagal kirim stiker-trigger '%s': %v", r.Label, err)
			return false
		}
		return true
	}

	// 2) Trigger TEKS.
	text := strings.TrimSpace(ctx.TextMessage)
	if text == "" {
		return false
	}
	r, ok := src.MatchRespon(text)
	if !ok {
		return false
	}
	if err := sendRespon(ctx, r); err != nil {
		ctx.Print("[RESPON] gagal kirim '%s': %v", r.Keyword, err)
		return false
	}
	return true
}

// sendRespon mengirim sebuah aturan respon (teks atau media) sebagai balasan
// (mengutip pesan pemicu).
func sendRespon(ctx *ContextBot, r src.Respon) error {
	if r.Type == src.ResponText {
		return ctx.Reply(r.Text)
	}

	data, err := src.GetResponMedia(r.Keyword)
	if err != nil || len(data) == 0 {
		// Media hilang → fallback teks bila ada.
		if r.Text != "" {
			return ctx.Reply(r.Text)
		}
		return fmt.Errorf("media kosong: %v", err)
	}

	uploadCtx, cancel := context.WithTimeout(ctx.Ctx, 60*time.Second)
	defer cancel()

	mediaKind := whatsmeow.MediaImage
	switch r.Type {
	case src.ResponVideo:
		mediaKind = whatsmeow.MediaVideo
	case src.ResponAudio:
		mediaKind = whatsmeow.MediaAudio
	}

	up, err := ctx.Client.Upload(uploadCtx, data, mediaKind)
	if err != nil {
		return fmt.Errorf("upload media: %w", err)
	}

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}
	quote := &waProto.ContextInfo{
		StanzaID:      proto.String(ctx.Msg.Info.ID),
		Participant:   proto.String(senderStr),
		QuotedMessage: ctx.Msg.Message,
	}

	mime := r.Mimetype
	var msg *waProto.Message
	switch r.Type {
	case src.ResponImage:
		if mime == "" {
			mime = "image/jpeg"
		}
		msg = &waProto.Message{ImageMessage: &waProto.ImageMessage{
			Caption: proto.String(r.Text), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, Mimetype: proto.String(mime), FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength), ContextInfo: quote,
		}}
	case src.ResponVideo:
		if mime == "" {
			mime = "video/mp4"
		}
		msg = &waProto.Message{VideoMessage: &waProto.VideoMessage{
			Caption: proto.String(r.Text), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, Mimetype: proto.String(mime), FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength), ContextInfo: quote,
		}}
	case src.ResponAudio:
		if mime == "" {
			mime = "audio/ogg; codecs=opus"
		}
		msg = &waProto.Message{AudioMessage: &waProto.AudioMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			Mimetype: proto.String(mime), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), ContextInfo: quote,
		}}
	case src.ResponSticker:
		msg = &waProto.Message{StickerMessage: &waProto.StickerMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			Mimetype: proto.String("image/webp"), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), ContextInfo: quote,
		}}
	default:
		return fmt.Errorf("tipe respon tak dikenal: %s", r.Type)
	}

	_, err = ctx.Client.SendMessage(ctx.Ctx, ctx.ChatJID.ToNonAD(), msg, src.AndroidExtra())
	return err
}

// ================= HELPER EKSTRAKSI MEDIA / TEKS DARI REPLY =================

// extractResponMedia mengambil media dari pesan yang di-reply (utamakan) atau pesan
// ini sendiri. Mengembalikan (tipe-respon, byte, mimetype, namaFile).
func extractResponMedia(ctx *ContextBot) (rtype string, data []byte, mime, filename string) {
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	return extractMediaFrom(ctx, quoted, ctx.Msg.Message)
}

// extractCurrentMedia hanya melihat media pada pesan INI (bukan yang di-reply).
// Dipakai trigger stiker: yang di-reply = pemicu, media balasan ada di pesan ini.
func extractCurrentMedia(ctx *ContextBot) (rtype string, data []byte, mime, filename string) {
	return extractMediaFrom(ctx, ctx.Msg.Message)
}

// extractMediaFrom mengunduh media pertama yang ditemukan dari daftar pesan (urut).
func extractMediaFrom(ctx *ContextBot, msgs ...*waProto.Message) (rtype string, data []byte, mime, filename string) {
	for _, m := range msgs {
		m = src.UnwrapMessage(m)
		if m == nil {
			continue
		}
		switch {
		case m.GetImageMessage() != nil:
			if d, err := ctx.Client.Download(context.Background(), m.GetImageMessage()); err == nil && len(d) > 0 {
				return src.ResponImage, d, m.GetImageMessage().GetMimetype(), "image.jpg"
			}
		case m.GetVideoMessage() != nil:
			if d, err := ctx.Client.Download(context.Background(), m.GetVideoMessage()); err == nil && len(d) > 0 {
				return src.ResponVideo, d, m.GetVideoMessage().GetMimetype(), "video.mp4"
			}
		case m.GetAudioMessage() != nil:
			if d, err := ctx.Client.Download(context.Background(), m.GetAudioMessage()); err == nil && len(d) > 0 {
				return src.ResponAudio, d, m.GetAudioMessage().GetMimetype(), "audio.ogg"
			}
		case m.GetStickerMessage() != nil:
			if d, err := ctx.Client.Download(context.Background(), m.GetStickerMessage()); err == nil && len(d) > 0 {
				return src.ResponSticker, d, "image/webp", "sticker.webp"
			}
		}
	}
	return "", nil, "", ""
}

// quotedSticker mengembalikan StickerMessage dari pesan yang di-reply (bila ada).
func quotedSticker(ctx *ContextBot) *waProto.StickerMessage {
	q := src.UnwrapMessage(ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage())
	if q == nil {
		return nil
	}
	return q.GetStickerMessage()
}

// stickerKey menurunkan kunci pencocokan stabil dari hash file stiker.
func stickerKey(sha []byte) string {
	return "stk:" + base64.StdEncoding.EncodeToString(sha)
}

// quotedText mengambil teks dari pesan yang di-reply (bila ada).
func quotedText(ctx *ContextBot) string {
	q := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	if q == nil {
		return ""
	}
	return strings.TrimSpace(src.ExtractTextMessage(src.UnwrapMessage(q)))
}

// quotedCaption mengambil caption media pesan yang di-reply (gambar/video).
func quotedCaption(ctx *ContextBot) string {
	q := src.UnwrapMessage(ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage())
	if q == nil {
		return ""
	}
	switch {
	case q.GetImageMessage() != nil:
		return strings.TrimSpace(q.GetImageMessage().GetCaption())
	case q.GetVideoMessage() != nil:
		return strings.TrimSpace(q.GetVideoMessage().GetCaption())
	}
	return ""
}

func responTypeIcon(t string) string {
	switch t {
	case src.ResponImage:
		return "🖼️"
	case src.ResponVideo:
		return "🎥"
	case src.ResponAudio:
		return "🎵"
	case src.ResponSticker:
		return "🔖"
	default:
		return "💬"
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// responTruncate memotong string secara rune-safe (aman emoji) dengan elipsis.
func responTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
