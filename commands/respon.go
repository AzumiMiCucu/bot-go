package commands

import (
	"context"
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
// keyword yang di-set owner. Balasan bisa TEKS atau MEDIA (gambar/video/audio/
// stiker). Disimpan di respon.db (lihat src/respondb.go).
//
//   respon add <keyword> [| teks]   → tambah (reply media untuk balasan media)
//   respon edit <keyword> [| teks]  → ubah (sama seperti add, menimpa)
//   respon del <keyword>            → hapus
//   respon list                     → daftar semua keyword
//   respon get <keyword>            → detail satu keyword
//   respon help                     → bantuan
//
// Alias praktis: addrespon / setrespon, editrespon, delrespon/hapusrespon,
// listrespon/responlist.
//
// Auto-respon AKTIF hanya saat bot TIDAK dalam mode self (lihat HandleAutoRespon).
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Respon",
		Category:    "Owner",
		Aliases:     []string{"respon", "response"},
		Pattern:     regexp.MustCompile(`(?i)^\s*respon(?:se)?(?:\s+(.+))?\s*$`),
		Description: "[Owner] Kelola auto-respon: add/edit/del/list/get",
		Execute:     ExecuteRespon,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Add Respon",
		Category:    "Owner",
		Aliases:     []string{"addrespon", "setrespon"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:addrespon|setrespon)(?:\s+(.+))?\s*$`),
		Description: "[Owner] Tambah auto-respon (reply media untuk balasan media)",
		Execute:     func(ctx *ContextBot) error { return responAdd(ctx, ctx.Args, false) },
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Edit Respon",
		Category:    "Owner",
		Aliases:     []string{"editrespon"},
		Pattern:     regexp.MustCompile(`(?i)^\s*editrespon(?:\s+(.+))?\s*$`),
		Description: "[Owner] Ubah auto-respon yang sudah ada",
		Execute:     func(ctx *ContextBot) error { return responAdd(ctx, ctx.Args, true) },
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Del Respon",
		Category:    "Owner",
		Aliases:     []string{"delrespon", "hapusrespon"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:delrespon|hapusrespon)(?:\s+(.+))?\s*$`),
		Description: "[Owner] Hapus auto-respon",
		Execute:     func(ctx *ContextBot) error { return responDel(ctx, ctx.Args) },
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "List Respon",
		Category:    "Owner",
		Aliases:     []string{"listrespon", "responlist"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:listrespon|responlist)\s*$`),
		Description: "[Owner] Daftar semua auto-respon",
		Execute:     func(ctx *ContextBot) error { return responList(ctx) },
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
		return ctx.Reply("⚠️ Keyword kosong. Contoh: `addrespon halo | Halo juga!`")
	}

	_, exists := src.GetRespon(keyword)
	if isEdit && !exists {
		return ctx.Reply(fmt.Sprintf("⚠️ Respon `%s` belum ada. Pakai `addrespon` untuk membuat baru.", keyword))
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
		return ctx.Reply("⚠️ Tidak ada isi balasan.\n\nGunakan salah satu:\n• `addrespon <keyword> | <teks balasan>`\n• reply sebuah pesan/media lalu ketik `addrespon <keyword>`")
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
		sb.WriteString(fmt.Sprintf("%d. %s `%s`%s%s\n", i+1, icon, r.Keyword, mt, preview))
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
	return "📖 *AUTO-RESPON*\n\n" +
		"`addrespon <keyword> | <teks>` — balasan teks\n" +
		"reply media + `addrespon <keyword>` — balasan media\n" +
		"`editrespon <keyword> ...` — ubah respon\n" +
		"`delrespon <keyword>` — hapus\n" +
		"`listrespon` — daftar semua\n" +
		"`respon get <keyword>` — detail\n\n" +
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
	text := strings.TrimSpace(ctx.TextMessage)
	if text == "" {
		return false
	}
	// Mode self (per-grup) → auto-respon dimatikan.
	if ctx.IsGroup && src.DB.IsGroupSelf(ctx.ChatJID.ToNonAD().String()) {
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
	for _, m := range []*waProto.Message{quoted, ctx.Msg.Message} {
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
