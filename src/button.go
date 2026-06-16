package src

import (
	"context"
	//"encoding/base64"
	"encoding/json"
//	"math"
	//"strconv"
//	"strings"
"fmt"
"crypto/rand"
"unicode"

//	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	
	// --- UPDATE BLOK PROTO DISINI ---
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	waAICommon "go.mau.fi/whatsmeow/proto/waAICommon"
	waAICommonDeprecated "go.mau.fi/whatsmeow/proto/waAICommonDeprecated"
	// ---------------------------------

	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type ButtonBuilder struct {
	title       string
	subtitle    string
	body        string
	footer      string
	buttons     []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
	contextInfo *waE2E.ContextInfo
	paramsJSON  []byte 
	mentions    []string 

	// === VARIABEL MEDIA ===
	mediaData []byte
	mediaType whatsmeow.MediaType
	mimeType  string
	fileName  string
}

func NewButton() *ButtonBuilder {
	return &ButtonBuilder{}
}

func (b *ButtonBuilder) SetTitle(title string) *ButtonBuilder {
	b.title = title
	return b
}

func (b *ButtonBuilder) SetSubtitle(subtitle string) *ButtonBuilder {
	b.subtitle = subtitle
	return b
}

func (b *ButtonBuilder) SetBody(body string) *ButtonBuilder {
	b.body = body
	return b
}

func (b *ButtonBuilder) SetFooter(footer string) *ButtonBuilder {
	b.footer = footer
	return b
}

func (b *ButtonBuilder) SetContextInfo(ci *waE2E.ContextInfo) *ButtonBuilder {
	b.contextInfo = ci
	return b
}

func (b *ButtonBuilder) SetParams(params interface{}) *ButtonBuilder {
	data, err := json.Marshal(params)
	if err == nil {
		b.paramsJSON = data
	}
	return b
}

func (b *ButtonBuilder) AddMentions(jids []string) *ButtonBuilder {
	b.mentions = jids
	return b
}

// ── MEDIA SETTERS ──

func (b *ButtonBuilder) SetImage(data []byte) *ButtonBuilder {
	b.mediaData = data
	b.mediaType = whatsmeow.MediaImage
	b.mimeType = "image/jpeg"
	return b
}

func (b *ButtonBuilder) SetVideo(data []byte) *ButtonBuilder {
	b.mediaData = data
	b.mediaType = whatsmeow.MediaVideo
	b.mimeType = "video/mp4"
	return b
}

func (b *ButtonBuilder) SetDocument(data []byte, fileName string) *ButtonBuilder {
	b.mediaData = data
	b.mediaType = whatsmeow.MediaDocument
	b.mimeType = "application/octet-stream"
	if fileName == "" {
		fileName = "document.bin"
	}
	b.fileName = fileName
	return b
}

// ── BUTTON TYPES ──

func (b *ButtonBuilder) AddReply(displayText, id string) *ButtonBuilder {
	params, _ := json.Marshal(map[string]interface{}{
		"display_text": displayText,
		"id":           id,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("quick_reply"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

func (b *ButtonBuilder) AddUrl(displayText, url string, webviewInteraction bool) *ButtonBuilder {
	paramsMap := map[string]interface{}{
		"display_text": displayText,
		"url":          url,
	}
	if webviewInteraction {
		paramsMap["webview_interaction"] = true
	}
	params, _ := json.Marshal(paramsMap)
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("cta_url"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

func (b *ButtonBuilder) AddCopy(displayText, copyCode, id string) *ButtonBuilder {
	params, _ := json.Marshal(map[string]interface{}{
		"display_text": displayText,
		"copy_code":    copyCode,
		"id":           id,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("cta_copy"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

func (b *ButtonBuilder) AddCall(displayText, phoneNumber string) *ButtonBuilder {
	params, _ := json.Marshal(map[string]interface{}{
		"display_text": displayText,
		"phone_number": phoneNumber,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("cta_call"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

func (b *ButtonBuilder) AddReminder(displayText, id string) *ButtonBuilder {
	params, _ := json.Marshal(map[string]interface{}{
		"display_text": displayText,
		"id":           id,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("cta_reminder"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// ✨ BARU: AddCancelReminder (Membatalkan pengingat)
func (b *ButtonBuilder) AddCancelReminder(displayText, id string) *ButtonBuilder {
	params, _ := json.Marshal(map[string]interface{}{
		"display_text": displayText,
		"id":           id,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("cta_cancel_reminder"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// ✨ BARU: AddAddress (Mengirim detail alamat/lokasi bisnis)
func (b *ButtonBuilder) AddAddress(displayText, id string) *ButtonBuilder {
	params, _ := json.Marshal(map[string]interface{}{
		"display_text": displayText,
		"id":           id,
	})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("address_message"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

func (b *ButtonBuilder) AddLocation() *ButtonBuilder {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("send_location"),
		ButtonParamsJSON: proto.String(""),
	})
	return b
}

// ── STRUCT HELPER UNTUK SELECTION ──
type SelectionRow struct {
	Header      string
	Title       string
	Description string
	ID          string
}

type SelectionSection struct {
	Title          string
	HighlightLabel string
	Rows           []SelectionRow
}

func (b *ButtonBuilder) AddSelection(title string, sections []SelectionSection) *ButtonBuilder {
	type row struct {
		Header      string `json:"header"`
		Title       string `json:"title"`
		Description string `json:"description"`
		ID          string `json:"id"`
	}
	type section struct {
		Title          string `json:"title"`
		HighlightLabel string `json:"highlight_label"`
		Rows           []row  `json:"rows"`
	}
	type selectionParams struct {
		Title    string    `json:"title"`
		Sections []section `json:"sections"`
	}

	var secs []section
	for _, s := range sections {
		var rows []row
		for _, r := range s.Rows {
			rows = append(rows, row{
				Header:      r.Header,
				Title:       r.Title,
				Description: r.Description,
				ID:          r.ID,
			})
		}
		secs = append(secs, section{
			Title:          s.Title,
			HighlightLabel: s.HighlightLabel,
			Rows:           rows,
		})
	}
	params, _ := json.Marshal(selectionParams{Title: title, Sections: secs})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("single_select"),
		ButtonParamsJSON: proto.String(string(params)),
	})
	return b
}

// ✨ BARU: ToCard (Mengonversi data Builder menjadi kartu murni untuk Carousel)
func (b *ButtonBuilder) ToCard(ctx *ContextBot) (*waE2E.InteractiveMessage, error) {
	header := &waE2E.InteractiveMessage_Header{
		Title:    proto.String(b.title),
		Subtitle: proto.String(b.subtitle),
	}

	if b.mediaData != nil {
		header.HasMediaAttachment = proto.Bool(true)
		resp, err := ctx.Client.Upload(context.Background(), b.mediaData, b.mediaType)
		if err != nil {
			return nil, err
		}
		switch b.mediaType {
		case whatsmeow.MediaImage:
			header.Media = &waE2E.InteractiveMessage_Header_ImageMessage{
				ImageMessage: &waE2E.ImageMessage{
					URL:           &resp.URL,
					DirectPath:    &resp.DirectPath,
					MediaKey:      resp.MediaKey,
					FileEncSHA256: resp.FileEncSHA256,
					FileSHA256:    resp.FileSHA256,
					FileLength:    &resp.FileLength,
					Mimetype:      proto.String(b.mimeType),
				},
			}
		case whatsmeow.MediaVideo:
			header.Media = &waE2E.InteractiveMessage_Header_VideoMessage{
				VideoMessage: &waE2E.VideoMessage{
					URL:           &resp.URL,
					DirectPath:    &resp.DirectPath,
					MediaKey:      resp.MediaKey,
					FileEncSHA256: resp.FileEncSHA256,
					FileSHA256:    resp.FileSHA256,
					FileLength:    &resp.FileLength,
					Mimetype:      proto.String(b.mimeType),
				},
			}
		case whatsmeow.MediaDocument:
			header.Media = &waE2E.InteractiveMessage_Header_DocumentMessage{
				DocumentMessage: &waE2E.DocumentMessage{
					URL:           &resp.URL,
					DirectPath:    &resp.DirectPath,
					MediaKey:      resp.MediaKey,
					FileEncSHA256: resp.FileEncSHA256,
					FileSHA256:    resp.FileSHA256,
					FileLength:    &resp.FileLength,
					Mimetype:      proto.String(b.mimeType),
					FileName:      proto.String(b.fileName),
				},
			}
		}
	}

	nativeFlowMsg := &waE2E.InteractiveMessage_NativeFlowMessage{
		Buttons: b.buttons,
	}
	if len(b.paramsJSON) > 0 {
		nativeFlowMsg.MessageParamsJSON = proto.String(string(b.paramsJSON))
	}

	return &waE2E.InteractiveMessage{
		Header: header,
		Body: &waE2E.InteractiveMessage_Body{
			Text: proto.String(b.body),
		},
		Footer: &waE2E.InteractiveMessage_Footer{
			Text: proto.String(b.footer),
		},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: nativeFlowMsg,
		},
	}, nil
}

// ── SEND ENGINE BUTTON ──

func (b *ButtonBuilder) Send(ctx *ContextBot, jid types.JID) error {
	_, err := b.SendWithID(ctx, jid)
	return err
}

// SendWithID mengirim button message dan mengembalikan ID pesan terkirim,
// agar pemanggil bisa mendaftarkannya ke reply-router (interaksi berbasis ID).
func (b *ButtonBuilder) SendWithID(ctx *ContextBot, jid types.JID) (string, error) {
	ci := b.contextInfo
	if ci == nil && ctx.Msg != nil {
		senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
		if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
			senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
		}
		ci = &waE2E.ContextInfo{
			StanzaID:      proto.String(ctx.Msg.Info.ID),
			Participant:   proto.String(senderStr),
			QuotedMessage: ctx.Msg.Message,
		}
	}
	if len(b.mentions) > 0 {
		if ci == nil {
			ci = &waE2E.ContextInfo{}
		}
		ci.MentionedJID = b.mentions
	}

	card, err := b.ToCard(ctx)
	if err != nil {
		return "", err
	}
	card.ContextInfo = ci

	msg := &waE2E.Message{
		ViewOnceMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				InteractiveMessage: card,
			},
		},
	}

	bizNode := waBinary.Node{
		Tag: "biz",
		Content: []waBinary.Node{
			{
				Tag:   "interactive",
				Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
				Content: []waBinary.Node{
					{Tag: "native_flow", Attrs: waBinary.Attrs{"v": "9", "name": "mixed"}},
				},
			},
		},
	}
	resp, err := ctx.Client.SendMessage(context.Background(), jid.ToNonAD(), msg, whatsmeow.SendRequestExtra{
		AdditionalNodes: &[]waBinary.Node{bizNode},
	})
	return string(resp.ID), err
}

func (b *ButtonBuilder) SendToChat(ctx *ContextBot) error {
	return b.Send(ctx, ctx.ChatJID)
}

func (b *ButtonBuilder) SendToChatWithID(ctx *ContextBot) (string, error) {
	return b.SendWithID(ctx, ctx.ChatJID)
}




// Helper: Generate UUID untuk payload AI
func generateAIRichUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Helper: Struct untuk AddProduct
type AIProduct struct {
	Title      string
	Brand      string
	Price      string
	SalePrice  string
	ProductURL string
	ImageURL   string
	IconURL    string
}

// Helper: Struct untuk AddPost
type AIPost struct {
	Title             string
	Subtitle          string
	Username          string
	ProfilePictureURL string
	IsVerified        bool
	ThumbnailURL      string
	PostCaption       string
	LikesCount        int
	CommentsCount     int
	SharesCount       int
	PostURL           string
	SourceApp         string
	PostType          string
}

// ── STRUCT UTAMA ──
type AIRichBuilder struct {
	title       string
	footer      string
	contextInfo *waE2E.ContextInfo
	submessages []*waAICommonDeprecated.AIRichResponseSubMessage
	sections    []map[string]interface{}
}

func NewAIRich() *AIRichBuilder {
	return &AIRichBuilder{
		submessages: []*waAICommonDeprecated.AIRichResponseSubMessage{},
		sections:    []map[string]interface{}{},
	}
}

// ── SETTERS ──

func (b *AIRichBuilder) SetTitle(title string) *AIRichBuilder {
	b.title = title
	return b
}

func (b *AIRichBuilder) SetFooter(footer string) *AIRichBuilder {
	b.footer = footer
	return b
}

func (b *AIRichBuilder) SetContextInfo(ci *waE2E.ContextInfo) *AIRichBuilder {
	b.contextInfo = ci
	return b
}

// ── CONTENT BUILDERS ──

func (b *AIRichBuilder) AddText(text string) *AIRichBuilder {
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageText: proto.String(text),
	})

	b.sections = append(b.sections, map[string]interface{}{
		"view_model": map[string]interface{}{
			"primitive": map[string]interface{}{
				"text":       text,
				"__typename": "GenAIMarkdownTextUXPrimitive",
			},
			"__typename": "GenAISingleLayoutViewModel",
		},
	})
	return b
}

func (b *AIRichBuilder) AddCode(language string, code string) *AIRichBuilder {
	// Simple Tokenizer (mirip dengan static tokenizer di JS)
	keywordsMap := map[string]bool{
		"break": true, "case": true, "catch": true, "const": true, "continue": true,
		"default": true, "delete": true, "do": true, "else": true, "export": true,
		"false": true, "finally": true, "for": true, "function": true, "if": true,
		"import": true, "in": true, "instanceof": true, "new": true, "null": true,
		"return": true, "super": true, "switch": true, "this": true, "throw": true,
		"true": true, "try": true, "typeof": true, "var": true, "void": true,
		"while": true, "with": true, "let": true, "static": true, "yield": true,
	}

	typeMap := map[int]string{0: "DEFAULT", 1: "KEYWORD", 2: "METHOD", 3: "STR", 4: "NUMBER", 5: "COMMENT"}
	var tokens []map[string]interface{}
	runes := []rune(code)
	i, length := 0, len(runes)

	for i < length {
		c := runes[i]
		if unicode.IsSpace(c) {
			s := i
			for i < length && unicode.IsSpace(runes[i]) { i++ }
			tokens = append(tokens, map[string]interface{}{"content": string(runes[s:i]), "type": "DEFAULT"})
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			s := i
			q := c
			i++
			for i < length {
				if runes[i] == '\\' && i+1 < length { i += 2 } else if runes[i] == q { i++; break } else { i++ }
			}
			tokens = append(tokens, map[string]interface{}{"content": string(runes[s:i]), "type": "STR"})
			continue
		}
		if unicode.IsLetter(c) || c == '_' || c == '$' {
			s := i
			for i < length && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_' || runes[i] == '$') { i++ }
			word := string(runes[s:i])
			typ := 0
			if keywordsMap[word] { typ = 1 }
			tokens = append(tokens, map[string]interface{}{"content": word, "type": typeMap[typ]})
			continue
		}
		tokens = append(tokens, map[string]interface{}{"content": string(c), "type": "DEFAULT"})
		i++
	}

	b.sections = append(b.sections, map[string]interface{}{
		"view_model": map[string]interface{}{
			"primitive": map[string]interface{}{
				"language":    language,
				"code_blocks": tokens,
				"__typename":  "GenAICodeUXPrimitive",
			},
			"__typename": "GenAISingleLayoutViewModel",
		},
	})
	return b
}

func (b *AIRichBuilder) AddProduct(products ...AIProduct) *AIRichBuilder {
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageText: proto.String("[ CANNOT_LOAD_PRODUCT - BOT ]"),
	})

	var primitives []map[string]interface{}
	for _, p := range products {
		primitives = append(primitives, map[string]interface{}{
			"title":       p.Title,
			"brand":       p.Brand,
			"price":       p.Price,
			"sale_price":  p.SalePrice,
			"product_url": p.ProductURL,
			"image":       map[string]interface{}{"url": p.ImageURL},
			"additional_images": []map[string]interface{}{
				{"url": p.IconURL},
			},
			"__typename": "GenAIProductItemCardPrimitive",
		})
	}

	if len(products) > 1 {
		b.sections = append(b.sections, map[string]interface{}{
			"view_model": map[string]interface{}{
				"primitives": primitives,
				"__typename": "GenAIHScrollLayoutViewModel",
			},
		})
	} else if len(products) == 1 {
		b.sections = append(b.sections, map[string]interface{}{
			"view_model": map[string]interface{}{
				"primitive":  primitives[0],
				"__typename": "GenAISingleLayoutViewModel",
			},
		})
	}
	return b
}

func (b *AIRichBuilder) AddPost(posts ...AIPost) *AIRichBuilder {
	b.submessages = append(b.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageText: proto.String("[ CANNOT_LOAD_POST - BOT ]"),
	})

	var primitives []map[string]interface{}
	for _, p := range posts {
		primitives = append(primitives, map[string]interface{}{
			"title":               p.Title,
			"subtitle":            p.Subtitle,
			"username":            p.Username,
			"profile_picture_url": p.ProfilePictureURL,
			"is_verified":         p.IsVerified,
			"thumbnail_url":       p.ThumbnailURL,
			"post_caption":        p.PostCaption,
			"likes_count":         p.LikesCount,
			"comments_count":      p.CommentsCount,
			"shares_count":        p.SharesCount,
			"post_url":            p.PostURL,
			"source_app":          p.SourceApp,
			"post_type":           p.PostType,
			"is_carousel":         len(posts) > 1,
			"__typename":          "GenAIPostPrimitive",
		})
	}

	b.sections = append(b.sections, map[string]interface{}{
		"view_model": map[string]interface{}{
			"primitives": primitives,
			"__typename": "GenAIHScrollLayoutViewModel", // Default HScroll untuk post
		},
	})
	return b
}

func (b *AIRichBuilder) AddSuggest(suggestions ...string) *AIRichBuilder {
	var pills []map[string]interface{}
	for _, text := range suggestions {
		pills = append(pills, map[string]interface{}{
			"prompt_text": text,
			"prompt_type": "SUGGESTED_PROMPT",
			"__typename":  "GenAIFollowUpSuggestionPillPrimitive",
		})
	}

	b.sections = append(b.sections, map[string]interface{}{
		"view_model": map[string]interface{}{
			"primitives": pills,
			"__typename": "GenAIActionRowLayoutViewModel",
		},
	})
	return b
}

// ── SEND ENGINE ──

func (b *AIRichBuilder) Send(ctx *ContextBot, jid types.JID) error {
	_, err := b.SendWithID(ctx, jid)
	return err
}

// SendWithID mengirim Rich UI dan mengembalikan ID pesan terkirim (untuk reply-router).
func (b *AIRichBuilder) SendWithID(ctx *ContextBot, jid types.JID) (string, error) {
	// Menambahkan footer jika di-set
	finalSections := append([]map[string]interface{}{}, b.sections...)
	if b.footer != "" {
		finalSections = append(finalSections, map[string]interface{}{
			"view_model": map[string]interface{}{
				"primitive": map[string]interface{}{
					"text":       b.footer,
					"__typename": "GenAIMetadataTextPrimitive",
				},
				"__typename": "GenAISingleLayoutViewModel",
			},
		})
	}

	payload := map[string]interface{}{
		"response_id": generateAIRichUUID(),
		"sections":    finalSections,
	}
	jsonData, _ := json.Marshal(payload)

	// ContextInfo Auto-Fill (Jika di-reply dari sebuah pesan)
	ci := b.contextInfo
	if ci == nil && ctx.Msg != nil {
		senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
		if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
			senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
		}
		ci = &waE2E.ContextInfo{
			StanzaID:      proto.String(ctx.Msg.Info.ID),
			Participant:   proto.String(senderStr),
			QuotedMessage: ctx.Msg.Message,
		}
	}

	// Fitur Forwarded Wajib Meta AI
	if ci == nil {
		ci = &waE2E.ContextInfo{}
	}
	ci.ForwardingScore = proto.Uint32(1)
	ci.IsForwarded = proto.Bool(true)
	ci.ForwardedAiBotMessageInfo = &waAICommon.ForwardedAIBotMessageInfo{
		BotJID: proto.String("0@bot"),
	}
	ci.ForwardOrigin = waE2E.ContextInfo_ForwardOrigin(4).Enum()

	msg := &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadata:        &waE2E.DeviceListMetadata{},
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata: &waAICommon.BotMetadata{
				MessageDisclaimerText: proto.String(b.title),
			},
		},
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: &waE2E.AIRichResponseMessage{
					MessageType: waAICommonDeprecated.AIRichResponseMessageType(1).Enum(),
					Submessages: b.submessages,
					UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
						Data: jsonData,
					},
					ContextInfo: ci,
				},
			},
		},
	}

	resp, err := ctx.Client.SendMessage(context.Background(), jid.ToNonAD(), msg)
	return string(resp.ID), err
}

func (b *AIRichBuilder) SendToChat(ctx *ContextBot) error {
	return b.Send(ctx, ctx.ChatJID)
}

func (b *AIRichBuilder) SendToChatWithID(ctx *ContextBot) (string, error) {
	return b.SendWithID(ctx, ctx.ChatJID)
}