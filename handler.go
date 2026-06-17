package main

import (
	"context"
	"fmt"
	"strings"
	"regexp"
	"time"

	"bot-go/commands"
	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// debugLogMessages mengaktifkan dump JSON penuh tiap pesan (berat). Matikan di produksi.
const debugLogMessages = false

func hasMediaContent(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	switch {
	case msg.ImageMessage != nil:
		return "🖼️ Gambar"
	case msg.VideoMessage != nil:
		return "🎥 Video"
	case msg.DocumentMessage != nil:
		return "📄 Dokumen"
	case msg.AudioMessage != nil:
		return "🎵 Audio"
	case msg.StickerMessage != nil:
		return "🔖 Stiker"
	}
	return ""
}

// getAnyContextInfo mengambil ContextInfo dari berbagai tipe pesan (teks/gambar/video).
func getAnyContextInfo(msg *waProto.Message) *waProto.ContextInfo {
	if msg == nil {
		return nil
	}
	if m := msg.GetExtendedTextMessage(); m != nil {
		return m.GetContextInfo()
	}
	if m := msg.GetImageMessage(); m != nil {
		return m.GetContextInfo()
	}
	if m := msg.GetVideoMessage(); m != nil {
		return m.GetContextInfo()
	}
	return nil
}

func ReplyMsg(client *whatsmeow.Client, chatJID types.JID, evt *events.Message, text string) error {
	_, err := sendReply(client, chatJID, evt, text)
	return err
}

// sendReply mengirim balasan (quote pesan asal) dan mengembalikan ID pesan terkirim.
// ID inilah yang dipakai sebagai kunci reply-router (interaksi berbasis ID pesan).
func sendReply(client *whatsmeow.Client, chatJID types.JID, evt *events.Message, text string) (string, error) {
	cleanTarget := chatJID.ToNonAD()
	cleanText := src.CleanUTF8(text)

	// Otomatis deteksi @angka dan masukkan ke array JID
	var mentionedJIDs []string
	mentionRegex := regexp.MustCompile(`@(\d+)`)
	matches := mentionRegex.FindAllStringSubmatch(cleanText, -1)
	for _, match := range matches {
		if len(match) > 1 {
			mentionedJIDs = append(mentionedJIDs, match[1]+"@lid")
		}
	}

	senderStr := evt.Info.Sender.ToNonAD().String()
	if !evt.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = evt.Info.MessageSource.SenderAlt.ToNonAD().String()
	}

	msgID := src.GenerateIOSMessageID()

	ctxInfo := &waProto.ContextInfo{
		StanzaID:      proto.String(evt.Info.ID),
		Participant:   proto.String(senderStr),
		QuotedMessage: evt.Message,
	}

	// Jika ada mention yang terdeteksi, lampirkan ke metadata pesan
	if len(mentionedJIDs) > 0 {
		ctxInfo.MentionedJID = mentionedJIDs
	}

	_, err := client.SendMessage(
		context.Background(),
		cleanTarget,
		&waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text:        proto.String(cleanText),
				ContextInfo: ctxInfo,
			},
		},
		whatsmeow.SendRequestExtra{
			ID: msgID,
		},
	)
	return string(msgID), err
}
func MessageHandler(client *whatsmeow.Client, evt *events.Message) {
	if evt.Message == nil || evt.Info.IsFromMe {
		return
	}
	if evt.Info.Sender.String() == "status@broadcast" {
		return
	}

	// =================================================================
	// 1. ADVANCED INTERCEPTOR: MULTI-GROUP & ANTI-TIMPA PESAN
	// =================================================================
	if src.HandleMetaAIResponse(client, evt) {
		return
	}
	// =================================================================

	textMessage := src.ExtractTextMessage(evt.Message)
	mediaType := hasMediaContent(evt.Message)

	src.Print("%s from %s", textMessage, evt.Info.Sender)
	if debugLogMessages {
		src.Print(evt.Message)
	}

	chatJID := evt.Info.Chat
	senderJID := evt.Info.Sender
	senderAlt := evt.Info.MessageSource.SenderAlt

	dbUserID := senderAlt.String()
	if senderAlt.IsEmpty() {
		dbUserID = senderJID.String()
	}

	user := strings.Split(dbUserID, "@")[0]
	isOwner := user == src.AppConfig.OwnerNumber
	pushName := evt.Info.PushName
	if pushName == "" {
		pushName = "User"
	}

	if textMessage == "" && mediaType == "" {
		return
	}

	// Simpan pesan ke Memori Konteks
	userMemory := commands.GetMemory(user)
	if textMessage != "" {
		userMemory.AddMessage(pushName, textMessage, map[string]interface{}{"isMedia": mediaType != ""})
	}

	// Ambil data user
	userData := src.DB.AddOrUpdateUser(dbUserID, pushName)
	if userData.IsBlocked {
		return
	}

	if evt.Info.IsGroup {
		// Eksekusi secara asinkron agar tidak memblokir respon bot
		go func() {
			isMedia := mediaType != ""
			wordCount := len(strings.Fields(textMessage))
			groupID := chatJID.ToNonAD().String()

			// 1. Catat statistik ke database
			src.DB.AddGroupStat(groupID, dbUserID, isMedia, wordCount)

			// 2. Tampung untuk auto-read batch tiap 30 menit
			src.AddPendingRead(evt.Info.Chat, evt.Info.Sender, evt.Info.ID, evt.Info.Timestamp)
		}()
	}

	// Command Parsing (hormati mode prefix bila aktif)
	cmdText := textMessage
	if src.AppConfig.PrefixMode {
		p := src.AppConfig.PrefixChar
		if p != "" && strings.HasPrefix(textMessage, p) {
			cmdText = strings.TrimSpace(textMessage[len(p):])
		} else {
			cmdText = "" // mode prefix aktif tapi tidak diawali prefix → bukan command
		}
	}
	matchedCommand, extractedArgs := commands.MatchCommand(cmdText)

	// Build Context (dibangun lebih awal agar bisa dipakai reply-router & eksekusi command)
	ctxBot := &commands.ContextBot{
		Client:      client,
		Msg:         evt,
		ChatJID:     chatJID,
		SenderJID:   senderJID,
		SenderAlt:   senderAlt,
		User:        user,
		Args:        extractedArgs,
		TextMessage: textMessage,
		PushName:    pushName,
		UserBalance: userData.Balance,
		IsGroup:     evt.Info.IsGroup,
		IsOwner:     isOwner,
		Memory:      userMemory,
		Ctx:         context.Background(),
		AddBalance: func(amount float64) float64 {
			return src.DB.AddBalance(dbUserID, amount)
		},
		DeductBalance: func(amount float64) (bool, float64) {
			return src.DB.DeductBalance(dbUserID, amount)
		},
		Reply: func(text string) error {
			return ReplyMsg(client, chatJID, evt, text)
		},
		ReplyWithID: func(text string) (string, error) {
			return sendReply(client, chatJID, evt, text)
		},
		React: func(text string) error {
			return src.ReactMessage(client, evt, text)
		},
		Print:  src.Print,
		Button: src.NewButton,
	}

	// =================================================================
	// ANTI-BOT: deteksi bot lain / flood → OTP challenge → kick.
	// Berjalan lebih dulu & tetap aktif walau mode self.
	// =================================================================
	if commands.HandleAntibot(ctxBot) {
		return
	}

	// =================================================================
	// MODE SELF: hanya owner yang dilayani (command & AI). Anti-bot tetap jalan.
	// =================================================================
	if src.AppConfig.BotMode == "self" && !isOwner {
		return
	}

	// =================================================================
	// REPLY-INTERACTION ROUTER (berbasis ID pesan bot, bukan sesi per-user)
	// Jika pesan ini me-reply sebuah pesan bot yang terdaftar, tangani di sini
	// lalu hentikan pipeline (tidak dicatat sebagai command).
	// =================================================================
	if commands.RouteReply(ctxBot) {
		return
	}

	// FALLBACK (AUTO AI JIKA BOT DI-TAG ATAU DI-REPLY)
	if matchedCommand == nil {
		if evt.Info.IsGroup {
			isMentioned := false
			cleanPrompt := textMessage

			botJID := client.Store.ID.ToNonAD().String()
			botLID := client.Store.LID.ToNonAD().String()

			ctxInfo := getAnyContextInfo(evt.Message)
			if ctxInfo != nil {
				for _, jid := range ctxInfo.GetMentionedJID() {
					if jid == botJID || jid == botLID {
						isMentioned = true
					}
					numberOnly := strings.Split(jid, "@")[0]
					cleanPrompt = strings.ReplaceAll(cleanPrompt, "@"+numberOnly, "")
				}
				// Trigger juga bila user me-reply pesan bot sendiri
				participant := ctxInfo.GetParticipant()
				if participant == botJID || participant == botLID {
					isMentioned = true
				}
			}

			cleanPrompt = strings.Map(func(r rune) rune {
				if r == '⁨' || r == '⁩' {
					return -1
				}
				return r
			}, cleanPrompt)
			cleanPrompt = strings.TrimSpace(cleanPrompt)

			if isMentioned && cleanPrompt != "" {
				// Cari gambar: di pesan ini, atau di pesan yang di-reply
				imgMsg := evt.Message.GetImageMessage()
				if imgMsg == nil && ctxInfo != nil {
					if q := ctxInfo.GetQuotedMessage(); q != nil && q.GetImageMessage() != nil {
						imgMsg = q.GetImageMessage()
					}
				}

				go func() {
					src.EnqueueRequest(chatJID, evt)

					if imgMsg != nil {
						// Mode gambar: download lalu teruskan ke Meta AI dengan caption prompt
						dlCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						data, err := client.Download(dlCtx, imgMsg)
						if err != nil || len(data) == 0 {
							_ = ReplyMsg(client, chatJID, evt, "❌ Gagal mengunduh gambar untuk diteruskan ke Meta AI.")
							return
						}
						mime := imgMsg.GetMimetype()
						// Catatan: SendMessage ke JID @bot sering mengembalikan error ack-timeout
						// walau pesan SUDAH terkirim & Meta AI tetap membalas. Maka error hanya
						// di-LOG, tidak dibalas ke grup (menghindari spam "❌ Gagal" palsu).
						if err := src.SendImageToMetaAI(client, cleanPrompt, data, mime); err != nil {
							fmt.Printf("⚠️ [META AI IMG] kirim selesai dengan warning: %v\n", err)
						}
						return
					}

					// Mode teks biasa — error hanya di-log (lihat catatan di atas).
					if err := src.SendTextToMetaAI(client, cleanPrompt); err != nil {
						fmt.Printf("⚠️ [META AI TEXT] kirim selesai dengan warning: %v\n", err)
					}
				}()
			}
		}
		return
	}

	// Cooldown
	if !matchedCommand.IsCooledDown(user) {
		_ = ReplyMsg(client, chatJID, evt, "⏱️ Cooldown aktif. Coba beberapa saat lagi.")
		return
	}

	fmt.Printf("⚙️ [EXECUTE] %s → '%s' (Args: %s)\n", pushName, matchedCommand.Name, extractedArgs)

	// Balance Check
	if matchedCommand.Price > 0 {
		success, remaining := src.DB.DeductBalance(dbUserID, matchedCommand.Price)
		if !success {
			_ = ReplyMsg(client, chatJID, evt, fmt.Sprintf(
				"💳 *SALDO TIDAK MENCUKUPI*\n\nFitur *%s* membutuhkan *$%.3f*.\nSaldo kamu: *$%.3f*",
				matchedCommand.Name, matchedCommand.Price, remaining,
			))
			return
		}
	}

	// Record Stats secara Asinkron
	go func() {
		_ = src.DB.AddCommandStat(matchedCommand.Name, dbUserID)
	}()

	matchedCommand.SetCooldown(user)

	// Indikator "mengetik" (human-like, fire-and-forget, tidak menunda balasan)
	go src.SendTyping(client, chatJID)

	// Execute Async dengan Middleware
	go func() {
		_ = commands.ExecuteHooks(commands.HookBeforeExecute, ctxBot, matchedCommand, nil)

		err := commands.ExecuteWithMiddlewares(ctxBot, matchedCommand)
		if err != nil {
			fmt.Printf("❌ [ERROR - %s]: %v\n", matchedCommand.Name, err)
			if matchedCommand.Price > 0 {
				src.DB.AddBalance(dbUserID, matchedCommand.Price)
				_ = ctxBot.Reply(fmt.Sprintf("⚠️ Gagal mengeksekusi *%s*. Saldo dikembalikan.\nError: %v", matchedCommand.Name, err))
			}
			_ = commands.ExecuteHooks(commands.HookOnError, ctxBot, matchedCommand, err)
		} else {
			_ = commands.ExecuteHooks(commands.HookAfterExecute, ctxBot, matchedCommand, nil)
		}
	}()
	

}
