package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

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

// reMention dipakai di SETIAP balasan untuk mendeteksi @angka. Dikompilasi SEKALI
// (package-level) — bukan tiap panggilan — agar jalur balas tetap secepat mungkin.
var reMention = regexp.MustCompile(`@(\d+)`)

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
	matches := reMention.FindAllStringSubmatch(cleanText, -1)
	for _, match := range matches {
		if len(match) > 1 {
			mentionedJIDs = append(mentionedJIDs, match[1]+"@lid")
		}
	}

	senderStr := evt.Info.Sender.ToNonAD().String()
	if !evt.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = evt.Info.MessageSource.SenderAlt.ToNonAD().String()
	}

	msgID := src.GenerateAndroidMessageID()

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

	textMessage := src.ExtractTextMessage(evt.Message)
	mediaType := hasMediaContent(evt.Message)

	// Logging + bookkeeping BERAT dijalankan ASINKRON — lepas TOTAL dari jalur respons:
	//   • src.Print (tulis stdout) tak lagi menyentuh hot-path → tak ada serialisasi
	//     antar-goroutine pesan di bawah beban.
	//   • DetectBot (analisis struktural + node) HANYA untuk GRUP — di situlah antibot
	//     & reputasi bot dipakai. Chat pribadi melewatinya demi respons paling cepat
	//     (cekbot di japri otomatis fallback ke analisis quote). Metadata raw (Message
	//     Secret/DeviceListMetadata) hanya hidup live → ditangkap di sini untuk cekbot.
	go func() {
		src.Print("%s from %s", textMessage, evt.Info.Sender)
		if debugLogMessages {
			src.Print(evt.Message)
		}
		if evt.Info.IsGroup {
			det := src.DetectBot(evt)
			src.CaptureDetection(det)
			// Reputasi bot per-akun ditulis SEKALI per pesan (hanya di sini) agar
			// hitungan rnqt tak ganda — antibot juga memanggil DetectBot (READ saja).
			src.CaptureReputation(det)
			src.StoreLiveMessage(det, evt.Info.Chat.ToNonAD().String(), textMessage, true, mediaType != "", evt.Info.Timestamp)
		}
	}()

	// Catat chat untuk auto-clear berkala (hapus dari tampilan akun bot tiap N menit).
	src.RecordChat(evt.Info.Chat, evt.Info.Sender, evt.Info.ID, evt.Info.IsFromMe, evt.Info.Timestamp)

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

	// Command Parsing.
	// Mode prefix: bila aktif, user SELAIN OWNER wajib mengawali dengan salah satu
	// prefix (PrefixChar bisa berisi banyak karakter, mis. ".!#"). Owner bebas prefix.
	cmdText := textMessage
	if src.AppConfig.PrefixMode && !isOwner {
		prefixes := src.AppConfig.PrefixChar
		if prefixes == "" {
			prefixes = "."
		}
		if len(textMessage) > 0 && strings.IndexByte(prefixes, textMessage[0]) >= 0 {
			cmdText = strings.TrimSpace(textMessage[1:])
		} else {
			cmdText = "" // non-owner tanpa prefix → bukan command
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
	// ANTILINK: hapus pesan berisi link terlarang (custom per grup).
	// =================================================================
	if commands.HandleAntilink(ctxBot) {
		return
	}

	// =================================================================
	// MODE SELF — scope TERPISAH untuk grup vs chat pribadi:
	//   • Grup  : per-grup (DEFAULT = SELF). Diubah lewat `self`/`public` di grup.
	//   • Japri : global PC (DEFAULT = PUBLIC). Diubah lewat `self`/`public` di japri.
	// Bila SELF, hanya owner dilayani (command/AI/respon). Anti-bot & antilink tetap
	// jalan (keduanya khusus grup & sudah dijalankan di atas).
	// =================================================================
	if !isOwner {
		if evt.Info.IsGroup {
			if src.DB.IsGroupSelf(chatJID.ToNonAD().String()) {
				return
			}
		} else if src.IsPrivateSelf() {
			return
		}
	}

	// =================================================================
	// REPLY-INTERACTION ROUTER (berbasis ID pesan bot, bukan sesi per-user)
	// Jika pesan ini me-reply sebuah pesan bot yang terdaftar, tangani di sini
	// lalu hentikan pipeline (tidak dicatat sebagai command).
	// =================================================================
	if commands.RouteReply(ctxBot) {
		return
	}

	// =================================================================
	// AUTO-RESPON (kustom owner): balas otomatis berdasarkan keyword tersimpan.
	// Hanya bila pesan BUKAN command (command menang) & bot tidak mode self
	// (dicek di dalam HandleAutoRespon). Berlaku untuk semua anggota grup publik.
	// =================================================================
	if matchedCommand == nil && commands.HandleAutoRespon(ctxBot) {
		return
	}

	// =================================================================
	// AGEN AI (owner, JAPRI): pesan natural (bukan command) → agen memilih
	// aksi otomatis (jadwal/tugas/email/catat/nilai/tanya). Hanya di chat
	// pribadi owner agar grup tidak terganggu.
	// =================================================================
	if matchedCommand == nil {
		if isOwner && !evt.Info.IsGroup && commands.HandleOwnerAgent(ctxBot) {
			return
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
