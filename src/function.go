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

// botMsgIDPatterns adalah heuristik pola message ID khas library bot (Baileys dkk),
// yang BUKAN dihasilkan WhatsApp mobile asli. Sengaja dibuat KONSERVATIF (berbasis
// prefix khas) agar tidak salah-tandai user WA biasa — sinyal utama tetap flood.
// Mudah ditambah/ditune sesuai temuan di lapangan.
var botMsgIDPatterns = []*regexp.Regexp{
    regexp.MustCompile(`^3EB0[0-9A-F]{16,}$`), // Baileys klasik (prefix 3EB0 + hex panjang)
    regexp.MustCompile(`^BAE5[0-9A-F]{10,}$`), // varian Baileys (prefix BAE5)
}

// LooksLikeBotMessageID mengembalikan true bila format ID cocok pola library bot.
func LooksLikeBotMessageID(id string) bool {
    for _, re := range botMsgIDPatterns {
        if re.MatchString(id) {
            return true
        }
    }
    return false
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
	)

	return err
}