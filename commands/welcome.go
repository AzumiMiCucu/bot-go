package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// WELCOME / GOODBYE — sambutan & perpisahan grup.
// Teks + jenis pesan bisa diatur: teks, stiker, gambar, atau video.
// Konfigurasi per-grup di group_settings (lihat src/group_settings.go).
// Media disimpan di database/greet/<grup>_<kind>.<ext> dan dikirim ulang
// tiap ada anggota masuk/keluar (event GroupInfo Join/Leave dari main.go).
//
// Placeholder teks: @user (mention) · {name} · {group} · {count}
// =================================================================

const greetMediaDir = "database/greet"

func init() {
	RegisterCommand(Command{
		Name:        "Welcome",
		Category:    "Group",
		Aliases:     []string{"welcome", "wc"},
		Pattern:     regexp.MustCompile(`(?is)^\s*(?:welcome|wc)(?:\s+(.+))?\s*$`),
		Description: "Atur sambutan anggota baru: on/off/text/set/test (admin)",
		Execute:     func(ctx *ContextBot) error { return executeGreet(ctx, "welcome") },
	}).Use(GroupOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Goodbye",
		Category:    "Group",
		Aliases:     []string{"goodbye", "bye"},
		Pattern:     regexp.MustCompile(`(?is)^\s*(?:goodbye|bye)(?:\s+(.+))?\s*$`),
		Description: "Atur perpisahan anggota keluar: on/off/text/set/test (admin)",
		Execute:     func(ctx *ContextBot) error { return executeGreet(ctx, "goodbye") },
	}).Use(GroupOnlyMiddleware)
}

func greetLabel(kind string) string {
	if kind == "goodbye" {
		return "Goodbye"
	}
	return "Welcome"
}

func defaultGreetText(kind string) string {
	if kind == "goodbye" {
		return "👋 Selamat tinggal @user, semoga sukses selalu!"
	}
	return "👋 Selamat datang @user di *{group}*!\nKamu anggota ke-{count}. Semoga betah ya."
}

// ====================== COMMAND ======================

func executeGreet(ctx *ContextBot, kind string) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur " + greetLabel(kind) + ".")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	raw := strings.TrimSpace(ctx.Args)
	fields := strings.Fields(raw)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}

	switch sub {
	case "", "show", "status":
		return greetShow(ctx, groupID, kind)
	case "on":
		src.DB.SetGreetOn(groupID, kind, true)
		return ctx.Reply("✅ " + greetLabel(kind) + " *diaktifkan*.")
	case "off":
		src.DB.SetGreetOn(groupID, kind, false)
		return ctx.Reply("🚫 " + greetLabel(kind) + " *dimatikan*.")
	case "text":
		rest := strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
		if rest == "" || strings.EqualFold(rest, "off") {
			src.DB.SetGreetText(groupID, kind, "")
			return ctx.Reply("🧹 Teks " + greetLabel(kind) + " dikosongkan.")
		}
		src.DB.SetGreetText(groupID, kind, rest)
		return ctx.Reply("✅ Teks " + greetLabel(kind) + " disimpan.\n\n_Placeholder:_ `@user` · `{name}` · `{group}` · `{count}`")
	case "set":
		return greetSetMedia(ctx, groupID, kind)
	case "media":
		if len(fields) > 1 && strings.EqualFold(fields[1], "off") {
			src.DB.ClearGreetMedia(groupID, kind)
			return ctx.Reply("🧹 Media " + greetLabel(kind) + " dihapus (kembali ke teks).")
		}
		return ctx.Reply("Pakai `" + kind + " set` (reply media) atau `" + kind + " media off`.")
	case "test":
		cfg := src.DB.GetGreet(groupID, kind)
		if cfg.Type == "" {
			cfg.Type = "text"
		}
		sendGreeting(ctx.Client, ctx.ChatJID, ctx.SenderJID.ToNonAD(), ctx.PushName, cfg, kind)
		return nil
	default:
		return ctx.Reply(greetHelp(kind))
	}
}

func greetHelp(kind string) string {
	k := kind
	return fmt.Sprintf("📖 *Pengaturan %s*\n\n"+
		"`%s on` / `%s off`\n"+
		"`%s text <isi>` — atur teks/caption\n"+
		"`%s set` — reply gambar/video/stiker untuk dijadikan media\n"+
		"`%s media off` — hapus media\n"+
		"`%s test` — kirim contoh\n\n"+
		"_Placeholder:_ `@user` `{name}` `{group}` `{count}`", greetLabel(kind), k, k, k, k, k, k)
}

func greetShow(ctx *ContextBot, groupID, kind string) error {
	cfg := src.DB.GetGreet(groupID, kind)
	status := "❌ OFF"
	if cfg.On {
		status = "✅ ON"
	}
	typ := cfg.Type
	if typ == "" {
		typ = "text"
	}
	txt := cfg.Text
	if txt == "" {
		txt = "_(default)_"
	}
	return ctx.Reply(fmt.Sprintf("⚙️ *%s*\n\nStatus : %s\nJenis  : `%s`\nTeks   : %s\n\n_Ketik `%s` tanpa argumen lain untuk bantuan: `%s help`._",
		greetLabel(kind), status, typ, txt, kind, kind))
}

func greetSetMedia(ctx *ContextBot, groupID, kind string) error {
	quoted := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	if quoted == nil {
		return ctx.Reply("⚠️ Reply sebuah *gambar / video / stiker* dengan `" + kind + " set`.")
	}

	var data []byte
	var err error
	var mtype, ext string
	switch {
	case quoted.GetImageMessage() != nil:
		data, err = ctx.Client.Download(context.Background(), quoted.GetImageMessage())
		mtype, ext = "image", ".jpg"
	case quoted.GetVideoMessage() != nil:
		data, err = ctx.Client.Download(context.Background(), quoted.GetVideoMessage())
		mtype, ext = "video", ".mp4"
	case quoted.GetStickerMessage() != nil:
		data, err = ctx.Client.Download(context.Background(), quoted.GetStickerMessage())
		mtype, ext = "sticker", ".webp"
	default:
		return ctx.Reply("⚠️ Jenis media tak didukung. Pakai gambar, video, atau stiker.")
	}
	if err != nil || len(data) == 0 {
		return ctx.Reply("❌ Gagal mengunduh media.")
	}

	if e := os.MkdirAll(greetMediaDir, 0o755); e != nil {
		return ctx.Reply("❌ Gagal menyiapkan folder media.")
	}
	path := filepath.Join(greetMediaDir, sanitizeFileName(groupID)+"_"+kind+ext)
	if e := os.WriteFile(path, data, 0o644); e != nil {
		return ctx.Reply("❌ Gagal menyimpan media.")
	}

	src.DB.SetGreetMedia(groupID, kind, mtype, path)
	return ctx.Reply(fmt.Sprintf("✅ Media %s (*%s*) disimpan.\nAktifkan dengan `%s on`, tambahkan teks via `%s text <isi>`.",
		greetLabel(kind), mtype, kind, kind))
}

var reNonFile = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitizeFileName(s string) string {
	return strings.Trim(reNonFile.ReplaceAllString(s, "_"), "_")
}

// ====================== EVENT HANDLER (dipanggil dari main.go) ======================

// HandleGroupJoin mengirim sambutan untuk tiap anggota baru.
func HandleGroupJoin(client *whatsmeow.Client, evt *events.GroupInfo) {
	cfg := src.DB.GetGreet(evt.JID.ToNonAD().String(), "welcome")
	if !cfg.On {
		return
	}
	for _, j := range evt.Join {
		sendGreeting(client, evt.JID, j, "", cfg, "welcome")
	}
}

// HandleGroupLeave mengirim perpisahan untuk tiap anggota yang keluar.
func HandleGroupLeave(client *whatsmeow.Client, evt *events.GroupInfo) {
	cfg := src.DB.GetGreet(evt.JID.ToNonAD().String(), "goodbye")
	if !cfg.On {
		return
	}
	for _, l := range evt.Leave {
		sendGreeting(client, evt.JID, l, "", cfg, "goodbye")
	}
}

// ====================== PENGIRIMAN ======================

func sendGreeting(client *whatsmeow.Client, group, target types.JID, targetName string, cfg src.GreetConfig, kind string) {
	text := cfg.Text
	if strings.TrimSpace(text) == "" {
		text = defaultGreetText(kind)
	}
	text = renderGreetText(client, group, target, targetName, text)
	mention := []string{target.String()}

	switch cfg.Type {
	case "image", "video":
		if !sendGreetMediaMsg(client, group, cfg, text, mention) {
			sendGreetText(client, group, text, mention) // fallback bila media hilang
		}
	case "sticker":
		sendGreetSticker(client, group, cfg)
		if strings.TrimSpace(cfg.Text) != "" {
			sendGreetText(client, group, text, mention)
		}
	default:
		sendGreetText(client, group, text, mention)
	}
}

// renderGreetText mengganti placeholder. @user → mention nomor; {name} → nama/no;
// {group} → nama grup; {count} → jumlah anggota.
func renderGreetText(client *whatsmeow.Client, group, target types.JID, targetName, text string) string {
	num := target.ToNonAD().User
	if targetName == "" {
		targetName = resolveContactName(client, target, num)
	}

	groupName, count := "grup ini", 0
	if info, err := client.GetGroupInfo(context.Background(), group); err == nil {
		if info.Name != "" {
			groupName = info.Name
		}
		count = len(info.Participants)
	}

	r := strings.NewReplacer(
		"@user", "@"+num,
		"{name}", targetName,
		"{group}", groupName,
		"{count}", strconv.Itoa(count),
	)
	return r.Replace(text)
}

func resolveContactName(client *whatsmeow.Client, target types.JID, fallback string) string {
	if client.Store != nil && client.Store.Contacts != nil {
		if c, err := client.Store.Contacts.GetContact(context.Background(), target); err == nil {
			if c.PushName != "" {
				return c.PushName
			}
			if c.FullName != "" {
				return c.FullName
			}
		}
	}
	return fallback
}

func sendGreetText(client *whatsmeow.Client, group types.JID, text string, mention []string) {
	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: &waProto.ContextInfo{MentionedJID: mention},
		},
	}
	client.SendMessage(context.Background(), group, msg, src.AndroidExtra())
}

func sendGreetMediaMsg(client *whatsmeow.Client, group types.JID, cfg src.GreetConfig, caption string, mention []string) bool {
	data, err := os.ReadFile(cfg.MediaPath)
	if err != nil || len(data) == 0 {
		return false
	}
	ctxUp, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var msg *waProto.Message
	if cfg.Type == "video" {
		up, e := client.Upload(ctxUp, data, whatsmeow.MediaVideo)
		if e != nil {
			return false
		}
		msg = &waProto.Message{VideoMessage: &waProto.VideoMessage{
			Caption: proto.String(caption), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, Mimetype: proto.String("video/mp4"),
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: &waProto.ContextInfo{MentionedJID: mention},
		}}
	} else {
		up, e := client.Upload(ctxUp, data, whatsmeow.MediaImage)
		if e != nil {
			return false
		}
		msg = &waProto.Message{ImageMessage: &waProto.ImageMessage{
			Caption: proto.String(caption), URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, Mimetype: proto.String("image/jpeg"),
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: &waProto.ContextInfo{MentionedJID: mention},
		}}
	}
	client.SendMessage(context.Background(), group, msg, src.AndroidExtra())
	return true
}

func sendGreetSticker(client *whatsmeow.Client, group types.JID, cfg src.GreetConfig) {
	data, err := os.ReadFile(cfg.MediaPath)
	if err != nil || len(data) == 0 {
		return
	}
	ctxUp, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	up, e := client.Upload(ctxUp, data, whatsmeow.MediaImage)
	if e != nil {
		return
	}
	msg := &waProto.Message{StickerMessage: &waProto.StickerMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		Mimetype: proto.String("image/webp"),
	}}
	client.SendMessage(context.Background(), group, msg, src.AndroidExtra())
}
