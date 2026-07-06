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

// reDevFlag menangkap flag -dev / --dev / -device di mana pun pada teks (dibuang
// dari teks yang dikirim). Bila ada → mode "exclude device non-primary": hanya HP
// utama (device 0) tiap anggota yang bisa membaca; WA Web/Desktop/tablet KOSONG.
var reDevFlag = regexp.MustCompile(`(?i)(?:^|\s)--?dev(?:ice)?\b`)

func init() {
	RegisterCommand(Command{
		Name:        "Sembunyi",
		Category:    "Group",
		Aliases:     []string{"sembunyi", "spr", "bisik", "hidden"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:sembunyi|spr|bisik|hidden)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Admin] Kirim pesan ke grup yang TIDAK diterima target (reply/tag). Tanpa target → exclude pengirim. Flag `-dev` → hanya HP utama tiap anggota yang bisa baca (WA Web/Desktop/tablet kosong).",
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

	raw := strings.TrimSpace(ctx.Args)

	// Deteksi & buang flag -dev (mode exclude device non-primary).
	devMode := reDevFlag.MatchString(raw)
	if devMode {
		raw = reDevFlag.ReplaceAllString(raw, " ")
	}

	// Teks yang dikirim = argumen, dengan token mention (@123) dibuang.
	text := reMentionTok.ReplaceAllString(raw, "")
	text = strings.TrimSpace(text)
	if text == "" {
		return ctx.Reply("✍️ Tulis pesannya.\n\n*Contoh:*\n› Reply pesan target lalu: `sembunyi <teks>`\n› Atau: `sembunyi <teks> @target`\n› Cuma tampil di HP utama semua orang: `sembunyi -dev <teks>`")
	}

	// Target yang di-exclude: mention + pesan yang di-reply.
	exclude := excludeTargets(ctx)

	_ = ctx.React("⏳")
	if devMode {
		// Mode -dev: buang seluruh device non-primary. Target eksplisit (mention/reply)
		// tetap dihormati bila ada; tanpa target, tak perlu meng-exclude pengirim —
		// tujuannya "hanya muncul di HP utama tiap orang".
		_, err = src.SendSecretTextDev(ctx.Ctx, ctx.Client, ctx.ChatJID, text, exclude, true)
	} else {
		// Mode normal: tanpa target → exclude pengirim sendiri.
		if len(exclude) == 0 {
			exclude = append(exclude, ctx.SenderJID)
		}
		_, err = src.SendSecretText(ctx.Ctx, ctx.Client, ctx.ChatJID, text, exclude, true)
	}
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
