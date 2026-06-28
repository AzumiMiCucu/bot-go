package commands

import (
	"context"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// ASISTEN AI — OWNER ONLY. Chat ke beberapa model AI dengan MEMORI percakapan
// per-owner & per-provider. Provider memakai antarmuka state yang sama
// (src.GetAIState/SetAIState/ResetAIState).
//
//	tanya <q>    → Gemini   (alias: ai, asisten)
//	gpt <q>      → ChatGPT  (alias: chatgpt)
//	copilot <q>  → Copilot  (alias: cpl)
//	<cmd> reset  → mulai percakapan baru
//
// Hanya owner yang bisa memakai. Fitur bot lain tak terpengaruh.
// =================================================================

// aiFunc menyamakan tanda tangan semua provider: (prompt, prevState) → (teks, state baru, err).
type aiFunc func(prompt, prevState string) (string, string, error)

func init() {
	RegisterCommand(Command{
		Name:        "Asisten AI (Gemini)",
		Category:    "Owner",
		Aliases:     []string{"tanya", "ai"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:tanya|ai)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Tanya AI Gemini dengan memori percakapan",
		Execute: func(ctx *ContextBot) error {
			return runAssistant(ctx, "gemini", "Gemini", func(p, s string) (string, string, error) {
				return src.GeminiChat(p, "Jawab ringkas, jelas, dan akurat dalam bahasa Indonesia.", s)
			})
		},
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Asisten AI (ChatGPT)",
		Category:    "Owner",
		Aliases:     []string{"gpt", "chatgpt"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:gpt|chatgpt)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Tanya ChatGPT dengan memori percakapan",
		Execute: func(ctx *ContextBot) error {
			return runAssistant(ctx, "chatgpt", "ChatGPT", src.ChatGPTChat)
		},
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Asisten AI (Copilot)",
		Category:    "Owner",
		Aliases:     []string{"copilot", "cpl"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:copilot|cpl)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Tanya Microsoft Copilot dengan memori percakapan",
		Execute:     runCopilot,
	}).Use(OwnerOnlyMiddleware)
}

// runCopilot menangani Copilot termasuk VISION: bila pesan menyertakan/me-reply
// gambar, gambar diunduh & dikirim ke Copilot bersama pertanyaan.
func runCopilot(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	img := aiExtractImage(ctx)

	if arg == "" && img == nil {
		return ctx.Reply("🤖 *ASISTEN AI — Copilot*\n\n" +
			"`copilot <pertanyaan>` — tanya (ingat konteks)\n" +
			"🖼️ kirim/*reply* gambar + `copilot <pertanyaan>` — analisa gambar (vision)\n" +
			"`copilot reset` — mulai percakapan baru")
	}
	switch strings.ToLower(arg) {
	case "reset", "clear", "baru", "new":
		src.ResetAIState("copilot", ctx.User)
		return ctx.Reply("🧹 Percakapan Copilot direset. Mulai dari awal.")
	}
	if arg == "" && img != nil {
		arg = "Jelaskan gambar ini secara ringkas dan jelas." // prompt default bila hanya gambar
	}

	_ = ctx.React("🤖")
	prev := src.GetAIState("copilot", ctx.User)
	text, newState, err := src.CopilotChat(arg, "chat", prev, img)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Copilot gagal: " + err.Error())
	}
	src.SetAIState("copilot", ctx.User, newState)
	_ = ctx.React("✅")
	return ctx.Reply(text)
}

// aiExtractImage mengunduh gambar dari pesan ini atau pesan yang di-reply (nil bila tak ada).
func aiExtractImage(ctx *ContextBot) *src.AIImage {
	m := src.UnwrapMessage(ctx.Msg.Message)
	if im := m.GetImageMessage(); im != nil {
		if d, err := ctx.Client.Download(context.Background(), im); err == nil && len(d) > 0 {
			return &src.AIImage{Data: d, Mime: im.GetMimetype()}
		}
	}
	q := src.UnwrapMessage(ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage())
	if q != nil {
		if im := q.GetImageMessage(); im != nil {
			if d, err := ctx.Client.Download(context.Background(), im); err == nil && len(d) > 0 {
				return &src.AIImage{Data: d, Mime: im.GetMimetype()}
			}
		}
	}
	return nil
}

// aiHasImage mengecek (tanpa download) apakah pesan menyertakan/me-reply gambar.
func aiHasImage(ctx *ContextBot) bool {
	if src.UnwrapMessage(ctx.Msg.Message).GetImageMessage() != nil {
		return true
	}
	q := src.UnwrapMessage(ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage())
	return q != nil && q.GetImageMessage() != nil
}

// runAssistant menjalankan satu provider: parse argumen, tangani `reset`, panggil
// fn dengan state tersimpan, lalu simpan state baru.
func runAssistant(ctx *ContextBot, provider, label string, fn aiFunc) error {
	arg := strings.TrimSpace(ctx.Args)
	if arg == "" {
		return ctx.Reply("🤖 *ASISTEN AI — " + label + "*\n\n" +
			"`<perintah> <pertanyaan>` — tanya AI (ingat konteks)\n" +
			"`<perintah> reset` — mulai percakapan baru\n\n" +
			"_Provider:_ `tanya` (Gemini), `gpt` (ChatGPT), `copilot` (Copilot).")
	}

	switch strings.ToLower(arg) {
	case "reset", "clear", "baru", "new":
		src.ResetAIState(provider, ctx.User)
		return ctx.Reply("🧹 Percakapan " + label + " direset. Mulai dari awal.")
	}

	_ = ctx.React("🤖")
	prev := src.GetAIState(provider, ctx.User)
	text, newState, err := fn(arg, prev)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ " + label + " gagal: " + err.Error())
	}
	src.SetAIState(provider, ctx.User, newState)
	_ = ctx.React("✅")
	if aiHasImage(ctx) {
		text += "\n\n_ℹ️ Gambar diabaikan — gunakan_ `copilot` _untuk menganalisa gambar (vision)._"
	}
	return ctx.Reply(text)
}
