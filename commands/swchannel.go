package commands

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waStatusAttributions"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// newsletterStatusContextInfo membuat ContextInfo dgn penanda story saluran.
// TEMUAN capture: story asli SELALU membawa statusAttributions=[NEWSLETTER_STATUS];
// TANPA ini server hanya ack tapi TIDAK mem-publish sbg story.
func newsletterStatusContextInfo() *waE2E.ContextInfo {
	t := waStatusAttributions.StatusAttribution_NEWSLETTER_STATUS
	return &waE2E.ContextInfo{
		StatusAttributions: []*waStatusAttributions.StatusAttribution{
			{Type: &t},
		},
	}
}

// randomMessageSecret: 32 byte acak untuk MessageContextInfo.MessageSecret
// (story asli selalu menyertakannya).
func randomMessageSecret() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

// =================================================================
// upswc — Up Status Saluran (Channel Status / Story WA Channel).
//
// TEMUAN dari capture (tmp/capture_channel_status/rawnode_*_status_*.json):
// story saluran TIBA ke follower sebagai node <status> (BUKAN <message>):
//
//   <status from="120..@newsletter" type="media" server_id=".." t="..">
//     <plaintext mediatype="image"><!-- protobuf ImageMessage --></plaintext>
//   </status>
//
// Isinya identik dgn post saluran biasa; beda-nya cuma TAG envelope
// (message → status). Maka utk POSTING story: kirim node <status> ke JID
// saluran. whatsmeow tak punya API utk ganti tag ini, jadi kita rakit node
// mentah & kirim via DangerousInternals().SendNode.
//
// Media saluran = PLAINTEXT (tanpa mediaKey/enc) → pakai UploadNewsletter,
// handle-nya jadi atribut media_id (persis jalur sendNewsletter).
//
// Syarat: akun bot harus ADMIN saluran tujuan.
//
// -----------------------------------------------------------------
// STATUS INVESTIGASI (2026-07-03) — DIJEDA sampai fitur RESMI.
// Semua jalur stanza sudah diuji terukur (baca ack via WaitResponse).
// Matriks hasil (server_id = post masuk sekuens newsletter = terbit):
//
//   KIRIM                                   | ACK                          | HASIL
//   ----------------------------------------+------------------------------+-----------------
//   <message to=nl> + attribution payload   | ack class=message, server_id | FEED (bukan ring)
//   <status  to=nl> + payload lengkap       | ack class=status, TANPA sid  | TIDAK terbit
//   <message to=nl> _type=status            | ack class=message, error=479 | DITOLAK
//   <message to=nl> +is_status/story/kategori| ack class=message, error=479 | DITOLAK
//
// KESIMPULAN TERUKUR:
//   • Diskriminator ring BUKAN tag envelope & BUKAN attribution payload.
//   • Server MEMVALIDASI KETAT node <message to=newsletter>: deviasi tipe /
//     atribut asing apa pun → error 479 (bukan diabaikan). Jadi "flag di
//     <message>" hampir pasti BUKAN mekanismenya.
//   • <status to=newsletter> diperlakukan seperti status pribadi (ack
//     class=status tanpa server_id) → tak pernah jadi story saluran.
//   • Node yg DI-DELIVER (feed=<message> vs story=<status>) hanya beda TAG,
//     tanpa atribut pembeda tersisa → flag pembeda dikonsumsi server-side.
//
// HIPOTESIS PALING MUNGKIN (belum diuji, butuh fitur resmi / capture klien):
//   channel-status dibuat via MUTATION Mex/GraphQL terpisah (spt create/
//   follow newsletter yg pakai query_id) — query_id-nya belum ada di
//   whatsmeow & hanya bisa didapat dgn menangkap trafik WhatsApp Web/Desktop.
//   Companion device TIDAK bisa melihat frame keluar app resmi (hanya terima
//   <status> hasil delivery yg sudah kita capture).
//
// KEPUTUSAN: hentikan pengejaran. Metode `probe` & `status` DIPERTAHANKAN
// sbg alat riset utk dilanjut saat fitur resmi. Jalur `msg` (default) tetap
// fungsional utk FEED saluran. Lihat memory feature-story-saluran.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Up Status Saluran",
		Category:    "Owner",
		Aliases:     []string{"upswc", "storysaluran", "csw"},
		Pattern:     regexp.MustCompile(`(?is)^\s*(?:upswc|storysaluran|csw)(?:\s+(.*))?$`),
		Description: "Upload teks/gambar/video sebagai story di saluran WA — owner, bot harus admin saluran",
		Execute:     UpStatusChannelCmd,
	}).Use(OwnerOnlyMiddleware)
}

func parseNewsletterJID(token string) (types.JID, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return types.JID{}, false
	}
	if !strings.Contains(token, "@") {
		if regexp.MustCompile(`^\d{8,}$`).MatchString(token) {
			token += "@" + types.NewsletterServer
		} else {
			return types.JID{}, false
		}
	}
	jid, err := types.ParseJID(token)
	if err != nil || jid.Server != types.NewsletterServer {
		return types.JID{}, false
	}
	return jid, true
}

func UpStatusChannelCmd(ctx *src.ContextBot) error {
	client := ctx.Client
	evt := ctx.Msg

	args := strings.TrimSpace(ctx.Args)
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return ctx.Reply("⚠️ Format:\n" +
			"• `upswc <jid@newsletter> <teks>`\n" +
			"• reply gambar/video → `upswc <jid@newsletter> [caption]`\n\n" +
			"Contoh: `upswc 120363410310246360@newsletter Halo`")
	}

	targetJID, ok := parseNewsletterJID(fields[0])
	if !ok {
		return ctx.Reply("⚠️ JID saluran tidak valid. Harus `...@newsletter` / angka saluran.")
	}
	rest := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))

	// Metode kirim (kata pertama setelah JID), di-parse DULU supaya sisa teks
	// jadi caption bersih:
	//   msg    → pipeline <message> (FEED, dapat server_id) [default]
	//   status → raw node <status> (ack class=status TANPA server_id → tak publish)
	//   probe  → raw <message to=newsletter> + ATRIBUT kustom utk riset diskriminator
	//            ring. Sintaks: `upswc <jid> probe k1=v1,k2=v2[,_tag=status][,_type=x] [caption]`
	//            key khusus: _tag override tag envelope, _type override attr type.
	method := "msg"
	probeSpec := ""
	if toks := strings.Fields(rest); len(toks) > 0 {
		switch toks[0] {
		case "msg", "status":
			method = toks[0]
			rest = strings.TrimSpace(strings.TrimPrefix(rest, toks[0]))
		case "probe":
			method = "probe"
			if len(toks) > 1 {
				probeSpec = toks[1]
				rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(rest, "probe")), probeSpec))
			} else {
				rest = strings.TrimSpace(strings.TrimPrefix(rest, "probe"))
			}
		}
	}

	quoted := evt.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()

	// Rakit payload + metadata node (type utk envelope, mediatype utk plaintext,
	// media_id = handle upload newsletter).
	var (
		payload     *waE2E.Message
		nodeType    = "text"
		mediaType   = ""
		mediaHandle = ""
	)

	switch {
	// --- Gambar (reply) ---
	case quoted.GetImageMessage() != nil:
		qim := quoted.GetImageMessage()
		data, e := client.Download(ctx.Ctx, qim)
		if e != nil || len(data) == 0 {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Gagal mengunduh gambar yang di-reply.")
		}
		up, e := client.UploadNewsletter(ctx.Ctx, data, whatsmeow.MediaImage)
		if e != nil {
			_ = ctx.React("❌")
			return ctx.Reply(fmt.Sprintf("❌ Gagal upload gambar ke saluran: %v", e))
		}
		caption := rest
		img := &waE2E.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(orDefault(qim.GetMimetype(), "image/jpeg")),
			JPEGThumbnail: qim.GetJPEGThumbnail(),
			ContextInfo:   newsletterStatusContextInfo(),
		}
		if h := qim.GetHeight(); h > 0 {
			img.Height = proto.Uint32(h)
		}
		if w := qim.GetWidth(); w > 0 {
			img.Width = proto.Uint32(w)
		}
		if caption != "" {
			img.Caption = proto.String(caption)
		}
		payload = &waE2E.Message{
			ImageMessage:       img,
			MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: randomMessageSecret()},
		}
		nodeType, mediaType, mediaHandle = "media", "image", up.Handle

	// --- Video (reply) ---
	case quoted.GetVideoMessage() != nil:
		qvm := quoted.GetVideoMessage()
		data, e := client.Download(ctx.Ctx, qvm)
		if e != nil || len(data) == 0 {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Gagal mengunduh video yang di-reply.")
		}
		up, e := client.UploadNewsletter(ctx.Ctx, data, whatsmeow.MediaVideo)
		if e != nil {
			_ = ctx.React("❌")
			return ctx.Reply(fmt.Sprintf("❌ Gagal upload video ke saluran: %v", e))
		}
		caption := rest
		vid := &waE2E.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(orDefault(qvm.GetMimetype(), "video/mp4")),
			JPEGThumbnail: qvm.GetJPEGThumbnail(),
			ContextInfo:   newsletterStatusContextInfo(),
		}
		if s := qvm.GetSeconds(); s > 0 {
			vid.Seconds = proto.Uint32(s)
		}
		if h := qvm.GetHeight(); h > 0 {
			vid.Height = proto.Uint32(h)
		}
		if w := qvm.GetWidth(); w > 0 {
			vid.Width = proto.Uint32(w)
		}
		if caption != "" {
			vid.Caption = proto.String(caption)
		}
		payload = &waE2E.Message{
			VideoMessage:       vid,
			MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: randomMessageSecret()},
		}
		nodeType, mediaType, mediaHandle = "media", "video", up.Handle

	// --- Teks ---
	case rest != "":
		payload = &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String(rest),
				ContextInfo: newsletterStatusContextInfo(),
			},
			MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: randomMessageSecret()},
		}
		nodeType = "text"

	default:
		return ctx.Reply("⚠️ Beri teks, atau reply gambar/video untuk dijadikan story saluran.")
	}

	_ = ctx.React("⏳")

	// --- Jalur RAW (status / probe): rakit node sendiri lalu BACA ack ---
	if method == "status" || method == "probe" {
		plaintext, merr := proto.Marshal(payload)
		if merr != nil {
			_ = ctx.React("❌")
			return ctx.Reply(fmt.Sprintf("❌ marshal: %v", merr))
		}
		ptNode := waBinary.Node{Tag: "plaintext", Content: plaintext, Attrs: waBinary.Attrs{}}
		if mediaType != "" {
			ptNode.Attrs["mediatype"] = mediaType
		}
		msgID := src.GenerateAndroidMessageID()
		tag := "status"
		attrs := waBinary.Attrs{"to": targetJID, "id": msgID, "type": nodeType}
		if mediaHandle != "" {
			attrs["media_id"] = mediaHandle
		}
		// probe: default tag <message> (satu-satunya yg selama ini beri server_id)
		// + atribut kustom utk mencari flag yg mengubah feed→ring.
		if method == "probe" {
			tag = "message"
			for _, kv := range strings.Split(probeSpec, ",") {
				kv = strings.TrimSpace(kv)
				if kv == "" {
					continue
				}
				k, v, _ := strings.Cut(kv, "=")
				switch {
				case k == "_tag":
					tag = v
				case k == "_type":
					attrs["type"] = v
				case v == "": // nilai kosong → HAPUS atribut (mis. `media_id=` buang media_id)
					delete(attrs, k)
				default:
					attrs[k] = v
				}
			}
		}
		node := waBinary.Node{Tag: tag, Attrs: attrs, Content: []waBinary.Node{ptNode}}

		ack, err := sendNodeAwaitAck(client, msgID, node, 10*time.Second)
		if err != nil {
			_ = ctx.React("⚠️")
			return ctx.Reply(fmt.Sprintf("⚠️ <%s> terkirim ke `%s` TAPI %v.", tag, targetJID.String(), err))
		}
		ag := ack.AttrGetter()
		serverID := ag.OptionalInt("server_id")
		errCode := ag.OptionalInt("error")
		ackXML := ack.XMLString()
		fmt.Printf("[UPSWC] method=%s node=<%s attrs=%v> ack: %s\n", method, tag, attrs, ackXML)

		switch {
		case errCode != 0:
			_ = ctx.React("❌")
			return ctx.Reply(fmt.Sprintf("❌ Server TOLAK <%s> (error %d) untuk `%s`.\n```\n%s\n```",
				tag, errCode, targetJID.String(), trimXML(ackXML)))
		case serverID > 0:
			_ = ctx.React("✅")
			return ctx.Reply(fmt.Sprintf("✅ <%s> PUBLISH! server_id: %d ke `%s`.\n"+
				"_Cek follower: muncul sbg RING atau FEED?_\n```\n%s\n```",
				tag, serverID, targetJID.String(), trimXML(ackXML)))
		default:
			_ = ctx.React("⚠️")
			return ctx.Reply(fmt.Sprintf("⚠️ <%s> di-ack TANPA server_id & TANPA error → tidak dipublish sbg post.\n"+
				"Ack mentah:\n```\n%s\n```", tag, trimXML(ackXML)))
		}
	}

	// Default: pipeline newsletter resmi (masuk feed, dapat server_id).
	extra := whatsmeow.SendRequestExtra{
		ID:          src.GenerateAndroidMessageID(),
		MediaHandle: mediaHandle,
	}
	resp, err := client.SendMessage(context.Background(), targetJID, payload, extra)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply(fmt.Sprintf("❌ Gagal kirim: %v\nPastikan bot ADMIN saluran.", err))
	}
	_ = ctx.React("✅")
	return ctx.Reply(fmt.Sprintf("✅ Dikirim via <message> ke `%s`\ntipe: %s | server_id: %v",
		targetJID.String(), nodeType, resp.ServerID))
}

// sendNodeAwaitAck mengirim node mentah lalu MENUNGGU ack server dgn id yg sama.
// DangerousInternals.SendNode fire-and-forget; kita daftarkan waiter dulu supaya
// bisa membaca ack (server_id/error). Mengembalikan node ack atau error.
func sendNodeAwaitAck(client *whatsmeow.Client, id string, node waBinary.Node, timeout time.Duration) (*waBinary.Node, error) {
	di := client.DangerousInternals()
	respChan := di.WaitResponse(id)
	if err := di.SendNode(context.Background(), node); err != nil {
		di.CancelResponse(id, respChan)
		return nil, fmt.Errorf("gagal SendNode: %w", err)
	}
	select {
	case ack := <-respChan:
		return ack, nil
	case <-time.After(timeout):
		di.CancelResponse(id, respChan)
		return nil, fmt.Errorf("tak ada ack dalam %s (server diam)", timeout)
	}
}

// trimXML memotong XML ack agar muat di balasan chat (diagnostik).
func trimXML(s string) string {
	const max = 700
	if len(s) > max {
		return s[:max] + "…(dipotong)"
	}
	return s
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
