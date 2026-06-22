package commands

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"bot-go/src"

	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waAICommonDeprecated"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Regex dipisah ke variabel agar bisa dipakai di dalam execute function
var channelCodeRegex = regexp.MustCompile(`(?is)^\s*(sendchannel)\s+([^|]+)\|(.*)$`)

var keywordsMap = map[string]map[string]bool{
	"javascript": {
		"break": true, "case": true, "catch": true, "continue": true, "debugger": true,
		"delete": true, "do": true, "else": true, "finally": true, "for": true,
		"function": true, "if": true, "in": true, "instanceof": true, "new": true,
		"return": true, "switch": true, "this": true, "throw": true, "try": true,
		"typeof": true, "var": true, "void": true, "while": true, "with": true,
		"true": true, "false": true, "null": true, "undefined": true, "class": true,
		"const": true, "let": true, "super": true, "extends": true, "export": true,
		"import": true, "yield": true, "static": true, "constructor": true, "async": true,
		"await": true, "get": true, "set": true,
	},
}

var typeMap = map[int]string{
	0: "DEFAULT",
	1: "KEYWORD",
	2: "METHOD",
	3: "STR",
	4: "NUMBER",
	5: "COMMENT",
}

func init() {
	RegisterCommand(Command{
		Name:        "Send Channel Code",
		Category:    "System",
		Aliases:     []string{"sendchannel"},
		Pattern:     channelCodeRegex, // Gunakan variabel global
		Description: "Mengirim blok kode berwarna ke Saluran",
		Execute:     SendChannelCodeMessageCmd,
	})
}

func tokenizeCodeChannel(code string, lang string) []map[string]interface{} {
	lang = strings.ToLower(strings.TrimSpace(lang))
	keywords, exists := keywordsMap[lang]
	if !exists {
		keywords = make(map[string]bool)
	}

	runes := []rune(code)
	length := len(runes)
	i := 0

	type Token struct {
		Content string
		Type    int
	}
	var tokens []Token

	push := func(content string, typ int) {
		if content == "" {
			return
		}
		if len(tokens) > 0 && tokens[len(tokens)-1].Type == typ {
			tokens[len(tokens)-1].Content += content
		} else {
			tokens = append(tokens, Token{Content: content, Type: typ})
		}
	}

	for i < length {
		c := runes[i]

		if unicode.IsSpace(c) {
			s := i
			for i < length && unicode.IsSpace(runes[i]) {
				i++
			}
			push(string(runes[s:i]), 0)
			continue
		}

		if c == '/' && i+1 < length && runes[i+1] == '/' {
			s := i
			i += 2
			for i < length && runes[i] != '\n' {
				i++
			}
			push(string(runes[s:i]), 5)
			continue
		}

		if c == '"' || c == '\'' || c == '`' {
			s := i
			q := c
			i++
			for i < length {
				if runes[i] == '\\' && i+1 < length {
					i += 2
				} else if runes[i] == q {
					i++
					break
				} else {
					i++
				}
			}
			push(string(runes[s:i]), 3)
			continue
		}

		if unicode.IsDigit(c) {
			s := i
			for i < length && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			push(string(runes[s:i]), 4)
			continue
		}

		isAlphaNum := func(r rune) bool {
			return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
		}
		if unicode.IsLetter(c) || c == '_' || c == '$' {
			s := i
			for i < length && isAlphaNum(runes[i]) {
				i++
			}
			word := string(runes[s:i])

			typ := 0
			if keywords[word] {
				typ = 1
			} else {
				j := i
				for j < length && unicode.IsSpace(runes[j]) {
					j++
				}
				if j < length && runes[j] == '(' {
					typ = 2
				}
			}
			push(word, typ)
			continue
		}

		push(string(c), 0)
		i++
	}

	var result []map[string]interface{}
	for _, t := range tokens {
		result = append(result, map[string]interface{}{
			"content": t.Content,
			"type":    typeMap[t.Type],
		})
	}
	return result
}

// NAMA FUNGSI DIUBAH AGAR TIDAK BENTROK
func generateUUIDChannel() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// NAMA FUNGSI DIUBAH AGAR TIDAK BENTROK
func buildCodeResponseChannelJSON(language string, codeBlocks []map[string]interface{}, footerText string) []byte {
	payload := map[string]interface{}{
		"response_id": generateUUIDChannel(),
		"sections": []map[string]interface{}{
			{
				"view_model": map[string]interface{}{
					"primitive": map[string]interface{}{
						"language":    language,
						"code_blocks": codeBlocks,
						"__typename":  "GenAICodeUXPrimitive",
					},
					"__typename": "GenAISingleLayoutViewModel",
				},
			},
			{
				"view_model": map[string]interface{}{
					"primitive": map[string]interface{}{
						"text":       footerText,
						"__typename": "GenAIMetadataTextPrimitive",
					},
					"__typename": "GenAISingleLayoutViewModel",
				},
			},
		},
	}
	jsonData, _ := json.Marshal(payload)
	return jsonData
}

func SendChannelCodeMessageCmd(ctx *src.ContextBot) error {
	client := ctx.Client
	evt := ctx.Msg

	textMessage := ""
	if evt.Message.GetExtendedTextMessage() != nil {
		textMessage = evt.Message.GetExtendedTextMessage().GetText()
	} else {
		textMessage = evt.Message.GetConversation()
	}

	// MENGGUNAKAN VARIABEL REGEX GLOBAL
	matches := channelCodeRegex.FindStringSubmatch(textMessage)
	if len(matches) < 4 {
		_, err := client.SendMessage(context.Background(), evt.Info.Chat, &waE2E.Message{
			Conversation: proto.String("❌ Format salah! Gunakan: sendchannel bahasa|kode"),
		})
		return err
	}

	language := strings.TrimSpace(matches[2])
	codeText := strings.TrimSpace(matches[3])
	footerText := "Generated by Bot"

	// JANGAN LUPA GANTI ID INI DENGAN ID CHANNEL ASLI
	channelID := "120363425670312915"
	targetChannelJID := types.NewJID(channelID, types.NewsletterServer)

	tokenizedCode := tokenizeCodeChannel(codeText, language)

	msg := &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadata:        &waE2E.DeviceListMetadata{},
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata: &waAICommon.BotMetadata{
				MessageDisclaimerText:       proto.String("Kode Channel"),
				RichResponseSourcesMetadata: &waAICommon.BotSourcesMetadata{},
			},
		},
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: &waE2E.AIRichResponseMessage{
					MessageType: waAICommonDeprecated.AIRichResponseMessageType(1).Enum(),
					Submessages: []*waAICommonDeprecated.AIRichResponseSubMessage{},
					UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
						// PAKAI FUNGSI YANG BARU DI-RENAME
						Data: buildCodeResponseChannelJSON(language, tokenizedCode, footerText),
					},
					ContextInfo: &waE2E.ContextInfo{
						ForwardingScore: proto.Uint32(1),
						IsForwarded:     proto.Bool(true),
						ForwardedAiBotMessageInfo: &waAICommon.ForwardedAIBotMessageInfo{
							BotJID: proto.String("0@bot"),
						},
						ForwardOrigin: waE2E.ContextInfo_ForwardOrigin(4).Enum(),
					},
				},
			},
		},
	}

	_, err := client.SendMessage(context.Background(), targetChannelJID, msg, AndroidExtra())
	if err != nil {
		fmt.Println("❌ Gagal mengirim ke Channel:", err)
		client.SendMessage(context.Background(), evt.Info.Chat, &waE2E.Message{
			Conversation: proto.String(fmt.Sprintf("Gagal mengirim ke channel: %v", err)),
		})
	} else {
		fmt.Println("✅ Kode berhasil dikirim ke Channel!")
		client.SendMessage(context.Background(), evt.Info.Chat, &waE2E.Message{
			Conversation: proto.String("✅ Kode berhasil dikirim ke Saluran!"),
		})
	}

	return err
}