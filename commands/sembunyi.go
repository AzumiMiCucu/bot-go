package commands

import (
	"context"
	"regexp"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// COMMAND: SEMBUNYI (setara sPR / relayMessage{exclude} di Baileys)
//
// Kirim teks ke grup TAPI target tertentu TIDAK menerimanya. Server WA hanya
// mem-fan-out pesan ke device yang ada di node <participants>, jadi membuang
// target dari daftar itu = target tak pernah dapat pesannya. Lihat
// src/exclude.go (SendGroupExcluding) untuk mekanismenya.
//
// Pemakaian (khusus admin grup / owner):
//   • reply pesan target lalu:  sembunyi <teks>
//   • atau tag target:          sembunyi <teks> @target
//   • tanpa target              → yang di-exclude adalah PENGIRIM sendiri
//
// Bot sendiri selalu di-exclude (excludeMe) agar pesan tak ikut tersinkron ke
// device bot lain.
// =================================================================

var reMentionTok = regexp.MustCompile(`@\d+`)

func init() {
	RegisterCommand(Command{
		Name:        "Sembunyi",
		Category:    "Group",
		Aliases:     []string{"sembunyi", "spr", "bisik", "hidden"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:sembunyi|spr|bisik|hidden)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Admin] Kirim pesan ke grup yang TIDAK diterima target (reply/tag). Tanpa target → exclude pengirim.",
		Execute:     ExecuteSembunyi,
	})
}

func ExecuteSembunyi(ctx *ContextBot) error {
	if !ctx.IsGroup {
		return ctx.Reply("⚠️ Perintah ini hanya untuk grup.")
	}

	gi, err := ctx.Client.GetGroupInfo(context.Background(), ctx.ChatJID)
	if err != nil {
		return ctx.Reply("❌ Gagal mengambil info grup.")
	}
	if !userIsAdminIn(ctx, gi) {
		return nil // bukan admin/owner → diam (hindari spam)
	}

	// Teks yang dikirim = argumen, dengan token mention (@123) dibuang.
	text := reMentionTok.ReplaceAllString(strings.TrimSpace(ctx.Args), "")
	text = strings.TrimSpace(text)
	if text == "" {
		return ctx.Reply("✍️ Tulis pesannya.\n\n*Contoh:*\n› Reply pesan target lalu: `sembunyi <teks>`\n› Atau: `sembunyi <teks> @target`")
	}

	// Target yang di-exclude: mention + pesan yang di-reply. Bila tak ada → pengirim.
	exclude := excludeTargets(ctx)
	if len(exclude) == 0 {
		exclude = append(exclude, ctx.SenderJID)
	}

	_ = ctx.React("⏳")
	_, err = src.SendSecretText(ctx.Ctx, ctx.Client, ctx.ChatJID, text, exclude, true)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengirim pesan tersembunyi.\n_" + err.Error() + "_")
	}
	_ = ctx.React("✅")
	return nil
}

// excludeTargets mengumpulkan JID dari mention dan dari pesan yang di-reply.
func excludeTargets(ctx *ContextBot) []types.JID {
	var out []types.JID
	seen := make(map[string]bool)
	add := func(raw string) {
		if raw == "" {
			return
		}
		if j, err := types.ParseJID(raw); err == nil {
			u := j.ToNonAD().User
			if u != "" && !seen[u] {
				seen[u] = true
				out = append(out, j.ToNonAD())
			}
		}
	}

	if ext := ctx.Msg.Message.GetExtendedTextMessage(); ext != nil {
		if ci := ext.GetContextInfo(); ci != nil {
			for _, m := range ci.GetMentionedJID() {
				add(m)
			}
			// Pesan yang di-reply → pengirim aslinya.
			add(ci.GetParticipant())
		}
	}
	return out
}
