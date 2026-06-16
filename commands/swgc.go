package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "Up Status Group",
		Category:    "Group",
		Aliases:     []string{"upswgc"},
		Pattern:     regexp.MustCompile(`(?i)^\s*upswgc(?:\s+(.*))?$`),
		Description: "Upload teks atau reply pesan ke status grup",
		Execute:     UpStatusGroupCmd,
	}).Use(OwnerOnlyMiddleware).Use(GroupOnlyMiddleware)
}

func UpStatusGroupCmd(ctx *src.ContextBot) error {
	client := ctx.Client
	evt := ctx.Msg
	chatJID := evt.Info.Chat

	text := ""
	if evt.Message.GetExtendedTextMessage() != nil {
		text = evt.Message.GetExtendedTextMessage().GetText()
	} else {
		text = evt.Message.GetConversation()
	}
	text = strings.TrimSpace(regexp.MustCompile(`(?i)^\s*[!/.]?upswgc\s*`).ReplaceAllString(text, ""))

	var payloadMsg *waE2E.Message

	if text != "" {
		payloadMsg = &waE2E.Message{
			Conversation: proto.String(text),
		}
	} else if evt.Message.GetExtendedTextMessage() != nil && evt.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage() != nil {
		payloadMsg = evt.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage()
	} else {
		_, err := client.SendMessage(context.Background(), chatJID, &waE2E.Message{
			Conversation: proto.String("⚠️ Berikan teks atau reply pesan yang ingin dijadikan status grup!"),
		})
		return err
	}

	msg := &waE2E.Message{
		GroupStatusMessageV2: &waE2E.FutureProofMessage{
			Message: payloadMsg,
		},
	}

	_, err := client.SendMessage(context.Background(), chatJID, msg)
	
	if err != nil {
		fmt.Println("❌ Gagal mengirim status grup:", err)
		client.SendMessage(context.Background(), chatJID, &waE2E.Message{
			Conversation: proto.String(fmt.Sprintf("⚠️ Error: %v", err)),
		})
	} else {
		client.SendMessage(context.Background(), chatJID, &waE2E.Message{
			Conversation: proto.String("✅ Berhasil upload ke status grup!"),
		})
	}

	return err
}