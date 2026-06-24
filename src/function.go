package src

import (
    "fmt"
	"unicode/utf8"
	"encoding/json"
		"crypto/rand"
	"encoding/hex"
	     	"strings"
	     "regexp"
	     "context"
	     "go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	    
	    
	)

func CleanUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	v := make([]rune, 0, len(s))
	for i, r := range s {
		if r == utf8.RuneError {
			_, size := utf8.DecodeRuneInString(s[i:])
			if size == 1 {
				continue
			}
		}
		v = append(v, r)
	}
	return string(v)
}


func Print(data ...interface{}) {
	if len(data) == 0 {
		return
	}

	// 1. Deteksi Format Teks yang Valid (bukan cuma % biasa, tapi %s, %v, %d, dsb)
	if firstStr, ok := data[0].(string); ok && len(data) > 1 {
		isFormat := false
		for _, verb := range []string{"%s", "%v", "%d", "%f", "%t", "%+v"} {
			if strings.Contains(firstStr, verb) {
				isFormat = true
				break
			}
		}
		if isFormat {
			fmt.Println(fmt.Sprintf(firstStr, data[1:]...))
			return
		}
	}

	// 2. Jika Hanya 1 Argumen (Cetak rapi / Pretty JSON)
	if len(data) == 1 {
		switch v := data[0].(type) {
		case string:
			fmt.Println(v)
		case []byte:
			fmt.Println(string(v))
		case int, int32, int64, uint, uint32, uint64, float32, float64, bool:
			fmt.Println(v)
		default:
			jd, _ := json.MarshalIndent(data[0], "", "  ")
			fmt.Println(string(jd))
		}
		return
	}

	// 3. Jika Banyak Argumen Biasa (Cetak sejajar / Inline)
	for i, val := range data {
		if i > 0 {
			fmt.Print(" ")
		}
		switch v := val.(type) {
		case string:
			fmt.Print(v)
		case int, int32, int64, uint, uint32, uint64, float32, float64, bool:
			fmt.Print(v)
		default:
			jd, _ := json.Marshal(val)
			fmt.Print(string(jd))
		}
	}
	fmt.Println()
}
func GenerateAndroidMessageID() types.MessageID {
    b := make([]byte, 15)
    rand.Read(b)
    return types.MessageID("AC" + strings.ToUpper(hex.EncodeToString(b)))
}

// AndroidExtra mengembalikan SendRequestExtra dengan message-ID custom format
// ANDROID ("AC"+30hex = 32 char). Pakai ini di SEMUA client.SendMessage agar
// semua pesan keluar konsisten ber-ID android.
func AndroidExtra() whatsmeow.SendRequestExtra {
    return whatsmeow.SendRequestExtra{ID: GenerateAndroidMessageID()}
}

// Klasifikasi tipe device dari FORMAT message-ID. Client WhatsApp asli menghasilkan
// ID dengan pola khas per-platform. ID yang tidak cocok pola mana pun ("unknown")
// sangat mungkin dari bot/library custom → dipakai anti-bot sebagai sinyal KUAT.
var (
    reDevIOS     = regexp.MustCompile(`^3A.{18}$`)       // iOS
    reDevWeb     = regexp.MustCompile(`^3E.{20}$`)       // WhatsApp Web
    reDevAndroid = regexp.MustCompile(`^(.{21}|.{32})$`) // Android ("AC"+30hex = 32 char = format ASLI WA Android)
    reDevDesktop = regexp.MustCompile(`^(3F|.{18}$)`)    // Desktop
)

// ClassifyDeviceFromID mengembalikan: ios | web | android | desktop | unknown.
// CATATAN: format "AC"+hex adalah message-ID ASLI WhatsApp Android — TIDAK bisa
// dipakai untuk membedakan bot dari HP Android sungguhan (bot pun memakainya).
func ClassifyDeviceFromID(id string) string {
    switch {
    case reDevIOS.MatchString(id):
        return "ios"
    case reDevWeb.MatchString(id):
        return "web"
    case reDevAndroid.MatchString(id):
        return "android"
    case reDevDesktop.MatchString(id):
        return "desktop"
    default:
        return "unknown"
    }
}

func ReactMessage(
	client *whatsmeow.Client,
	evt *events.Message,
	emoji string,
) error {

	_, err := client.SendMessage(
		context.Background(),
		evt.Info.Chat,
		&waProto.Message{
			ReactionMessage: &waProto.ReactionMessage{
				Key: &waProto.MessageKey{
					RemoteJID: proto.String(evt.Info.Chat.String()),
					FromMe:    proto.Bool(evt.Info.IsFromMe),
					ID:        proto.String(evt.Info.ID),
					Participant: func() *string {
						if evt.Info.IsGroup {
							s := evt.Info.Sender.String()
							return &s
						}
						return nil
					}(),
				},
				Text: proto.String(emoji),
			},
		},
		AndroidExtra(),
	)

	return err
}

// ExtractTextMessage mengekstrak konten teks dari berbagai tipe pesan WA.
// Dibutuhkan oleh handler.go (sebelumnya di meta.go).
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