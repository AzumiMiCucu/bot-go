package src

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"


	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type PendingAIRequest struct {
	GroupJID   types.JID
	OrigEvt    *events.Message
	EnqueuedAt time.Time
}

type AIResponseTracker struct {
	GroupJID    types.JID
	OrigEvt     *events.Message
	LastText    string
	PendingText string // teks terbaru yang datang saat balasan pertama masih dikirim
}

var (
	MetaAIQueue          []PendingAIRequest
	MetaAIMsgTracker     = make(map[string]*AIResponseTracker)
	GroupToBotMsgID      = make(map[string]string)
	CurrentActiveTracker *AIResponseTracker
	MetaAIMutex          sync.Mutex
)

const MetaAINumber = "867051314767696"

func IsMetaAIChannel(client *whatsmeow.Client, evt *events.Message) bool {
	chatStr := evt.Info.Chat.String()
	senderStr := evt.Info.Sender.String()
	botID := client.Store.ID.ToNonAD().String()

	var botLID string
	if !client.Store.LID.IsEmpty() {
		botLID = client.Store.LID.ToNonAD().String()
	}

	if strings.Contains(chatStr, MetaAINumber) || strings.Contains(senderStr, MetaAINumber) {
		return true
	}
	if strings.Contains(chatStr, botID) || strings.Contains(senderStr, botID) {
		return true
	}
	if botLID != "" && (strings.Contains(chatStr, botLID) || strings.Contains(senderStr, botLID)) {
		return true
	}
	if evt.Info.Chat.Server == "bot" || evt.Info.Sender.Server == "bot" {
		return true
	}
	return false
}

func ForwardMedia(client *whatsmeow.Client, targetJID types.JID, origEvt *events.Message, msg *waProto.Message) error {
	var data []byte
	var err error
	var captionText string

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if msg.ImageMessage != nil {
		data, err = client.Download(ctx, msg.ImageMessage)
		captionText = msg.ImageMessage.GetCaption()
	} else {
		return nil
	}

	if err != nil || len(data) == 0 {
		return fmt.Errorf("gagal download media dari Meta: %v", err)
	}

	resp, err := client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("gagal upload media ke grup: %v", err)
	}

	senderStr := origEvt.Info.Sender.ToNonAD().String()
	if !origEvt.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = origEvt.Info.MessageSource.SenderAlt.ToNonAD().String()
	}
	ctxInfo := &waProto.ContextInfo{
		StanzaID:      proto.String(origEvt.Info.ID),
		Participant:   proto.String(senderStr),
		QuotedMessage: origEvt.Message,
	}

	protoMsg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			Caption:       proto.String(captionText),
			URL:           proto.String(resp.URL),
			DirectPath:    proto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			Mimetype:      msg.ImageMessage.Mimetype,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			ContextInfo:   ctxInfo,
		},
	}

	_, err = client.SendMessage(ctx, targetJID, protoMsg)
	return err
}

func ReplyMsgWithID(client *whatsmeow.Client, chatJID types.JID, evt *events.Message, text string) (string, error) {
	cleanTarget := chatJID.ToNonAD()
	cleanText := CleanUTF8(text)
	senderStr := evt.Info.Sender.ToNonAD().String()
	if !evt.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = evt.Info.MessageSource.SenderAlt.ToNonAD().String()
	}
	msgID := NewMessageID(client)
	_, err := client.SendMessage(
		context.Background(),
		cleanTarget,
		&waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String(cleanText),
				ContextInfo: &waProto.ContextInfo{
					StanzaID:      proto.String(evt.Info.ID),
					Participant:   proto.String(senderStr),
					QuotedMessage: evt.Message,
				},
			},
		},
		whatsmeow.SendRequestExtra{ID: msgID},
	)
	return msgID, err
}

func EditGroupMsg(client *whatsmeow.Client, chatJID types.JID, targetMsgID string, newText string) error {
	cleanText := CleanUTF8(newText)
	editProto := &waProto.ProtocolMessage{
		Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key: &waProto.MessageKey{
			FromMe:    proto.Bool(true),
			ID:        proto.String(targetMsgID),
			RemoteJID: proto.String(chatJID.ToNonAD().String()),
		},
		EditedMessage: &waProto.Message{
			Conversation: proto.String(cleanText),
		},
	}
	_, err := client.SendMessage(context.Background(), chatJID.ToNonAD(), &waProto.Message{
		ProtocolMessage: editProto,
	})
	return err
}

func ExtractTextMessage(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	switch {
	case msg.Conversation != nil:
		return strings.TrimSpace(msg.GetConversation())
	case msg.ExtendedTextMessage != nil:
		return strings.TrimSpace(msg.ExtendedTextMessage.GetText())
	case msg.ImageMessage != nil:
		return strings.TrimSpace(msg.ImageMessage.GetCaption())
	case msg.VideoMessage != nil:
		return strings.TrimSpace(msg.VideoMessage.GetCaption())
	case msg.DocumentMessage != nil:
		return strings.TrimSpace(msg.DocumentMessage.GetCaption())
	case msg.ReactionMessage != nil:
		return strings.TrimSpace(msg.ReactionMessage.GetText())
	case msg.TemplateButtonReplyMessage != nil:
		return strings.TrimSpace(msg.TemplateButtonReplyMessage.GetSelectedID())
	case msg.ProtocolMessage != nil && msg.ProtocolMessage.EditedMessage != nil:
		em := msg.ProtocolMessage.EditedMessage
		if r := em.RichResponseMessage; r != nil && len(r.Submessages) > 0 {
			return strings.TrimSpace(r.Submessages[0].GetMessageText())
		}
		if em.ExtendedTextMessage != nil {
			return strings.TrimSpace(em.ExtendedTextMessage.GetText())
		}
		return strings.TrimSpace(em.GetConversation())
	}
	return ""
}

func HandleMetaAIResponse(client *whatsmeow.Client, evt *events.Message) bool {
	if !IsMetaAIChannel(client, evt) {
		return false
	}

	MetaAIMutex.Lock()
	defer MetaAIMutex.Unlock()

	var contentMsg *waProto.Message
	var isEdit bool
	var metaMsgID string

	if evt.Message.ProtocolMessage != nil && evt.Message.ProtocolMessage.GetType() == waProto.ProtocolMessage_MESSAGE_EDIT {
		contentMsg = evt.Message.ProtocolMessage.EditedMessage
		isEdit = true
		metaMsgID = evt.Message.ProtocolMessage.Key.GetID()
	} else {
		contentMsg = evt.Message
		isEdit = false
		metaMsgID = evt.Info.ID
	}

	if contentMsg == nil {
		return true
	}

	isMedia := contentMsg.ImageMessage != nil || contentMsg.VideoMessage != nil || contentMsg.StickerMessage != nil
	text := ExtractTextMessage(contentMsg)

	var targetTracker *AIResponseTracker

	if isEdit {
		if tracker, exists := MetaAIMsgTracker[metaMsgID]; exists {
			targetTracker = tracker
		} else {
			return true
		}
	} else {
		isImageProgress := isMedia || strings.Contains(text, "Creating your image") || strings.Contains(text, "Your image is ready")

		if isImageProgress && CurrentActiveTracker != nil {
			targetTracker = CurrentActiveTracker
			MetaAIMsgTracker[metaMsgID] = targetTracker
		} else {
			// Buang entri queue yang sudah basi (>2 menit) agar tidak salah pasang
			for len(MetaAIQueue) > 0 && time.Since(MetaAIQueue[0].EnqueuedAt) > 2*time.Minute {
				MetaAIQueue = MetaAIQueue[1:]
			}

			if len(MetaAIQueue) > 0 {
				req := MetaAIQueue[0]
				MetaAIQueue = MetaAIQueue[1:]

				targetTracker = &AIResponseTracker{
					GroupJID: req.GroupJID,
					OrigEvt:  req.OrigEvt,
				}
				CurrentActiveTracker = targetTracker
				MetaAIMsgTracker[metaMsgID] = targetTracker
			} else if CurrentActiveTracker != nil {
				targetTracker = CurrentActiveTracker
				MetaAIMsgTracker[metaMsgID] = targetTracker
			} else {
				return true
			}
		}
		GroupToBotMsgID[targetTracker.GroupJID.String()] = ""
	}

	targetJID := targetTracker.GroupJID
	origEvt := targetTracker.OrigEvt

	if isMedia {
		go func() {
			err := ForwardMedia(client, targetJID, origEvt, contentMsg)
			if err != nil {
				fmt.Printf("❌ [MEDIA ERROR]: %v\n", err)
			}
		}()
		return true
	} else if text != "" {
		botMsgID := GroupToBotMsgID[targetJID.String()]

		if botMsgID == "" {
			GroupToBotMsgID[targetJID.String()] = "PENDING"
			// Unlock for network call
			MetaAIMutex.Unlock()
			newID, err := ReplyMsgWithID(client, targetJID, origEvt, text)
			MetaAIMutex.Lock()

			if err == nil {
				GroupToBotMsgID[targetJID.String()] = newID
				targetTracker.LastText = text

				// Terapkan teks yang datang saat masih PENDING (anti-drop)
				if targetTracker.PendingText != "" && targetTracker.PendingText != text {
					pending := targetTracker.PendingText
					targetTracker.PendingText = ""
					targetTracker.LastText = pending
					MetaAIMutex.Unlock()
					_ = EditGroupMsg(client, targetJID, newID, pending)
					MetaAIMutex.Lock()
				}
			} else {
				GroupToBotMsgID[targetJID.String()] = ""
			}
		} else if botMsgID == "PENDING" {
			// Balasan pertama masih dikirim — simpan teks terbaru agar tidak hilang
			targetTracker.PendingText = text
			return true
		} else {
			if targetTracker.LastText == text {
				return true
			}
			targetTracker.LastText = text
			// Unlock for network call
			MetaAIMutex.Unlock()
			_ = EditGroupMsg(client, targetJID, botMsgID, text)
			MetaAIMutex.Lock()
		}
	}

	return true
}

func EnqueueRequest(groupJID types.JID, evt *events.Message) {
	MetaAIMutex.Lock()
	defer MetaAIMutex.Unlock()
	MetaAIQueue = append(MetaAIQueue, PendingAIRequest{
		GroupJID:   groupJID,
		OrigEvt:    evt,
		EnqueuedAt: time.Now(),
	})
}

// SendTextToMetaAI meneruskan pertanyaan teks ke Meta AI.
func SendTextToMetaAI(client *whatsmeow.Client, prompt string) error {
	metaAIJID := types.NewJID(MetaAINumber, "bot")
	_, err := client.SendMessage(context.Background(), metaAIJID, &waProto.Message{
		Conversation: proto.String(prompt),
	})
	return err
}

// SendImageToMetaAI mengunggah gambar lalu mengirimkannya (beserta caption/prompt)
// ke channel Meta AI agar bot bisa "melihat" gambar, bukan sekadar teks.
func SendImageToMetaAI(client *whatsmeow.Client, prompt string, imageData []byte, mimetype string) error {
	if len(imageData) == 0 {
		return fmt.Errorf("data gambar kosong")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Upload(ctx, imageData, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("gagal upload gambar ke Meta AI: %v", err)
	}

	if mimetype == "" {
		mimetype = "image/jpeg"
	}

	metaAIJID := types.NewJID(MetaAINumber, "bot")
	protoMsg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			Caption:       proto.String(prompt),
			URL:           proto.String(resp.URL),
			DirectPath:    proto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			Mimetype:      proto.String(mimetype),
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(imageData))),
		},
	}

	_, err = client.SendMessage(ctx, metaAIJID, protoMsg)
	return err
}
