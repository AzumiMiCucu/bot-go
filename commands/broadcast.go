package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// BROADCAST (bc) — kirim pesan ke SEMUA grup yang diikuti bot (owner).
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Broadcast",
		Category:    "Owner",
		Aliases:     []string{"bc", "broadcast"},
		Pattern:     regexp.MustCompile(`(?is)^(?:bc|broadcast)\s+(.+)$`),
		Description: "Kirim pesan ke semua grup yang diikuti bot (owner)",
		Execute:     ExecuteBroadcast,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteBroadcast(ctx *ContextBot) error {
	msg := strings.TrimSpace(ctx.Args)
	if msg == "" {
		return ctx.Reply("⚠️ Format: `bc <pesan>`")
	}

	_ = ctx.React("📢")

	// Jalankan di background (broadcast ke banyak grup butuh waktu karena ada jeda).
	go func() {
		sent, total := broadcastText(ctx.Client, msg)
		_ = ctx.Reply(fmt.Sprintf("📢 *BROADCAST SELESAI*\nTerkirim ke *%d/%d* grup.", sent, total))
	}()
	return nil
}

// broadcastText mengirim teks ke semua grup. Context per-grup agar satu kegagalan
// tidak membatalkan sisanya. Mengembalikan (terkirim, total).
func broadcastText(client *whatsmeow.Client, text string) (int, int) {
	listCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	groups, err := client.GetJoinedGroups(listCtx)
	cancel()
	if err != nil {
		fmt.Printf("[BC] gagal ambil daftar grup: %v\n", err)
		return 0, 0
	}
	fmt.Printf("[BC] broadcast ke %d grup...\n", len(groups))

	sent := 0
	for _, g := range groups {
		sendCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		// WAJIB: refresh daftar peserta/device dulu agar pesan tidak salah-target
		// (mencegah "participant list hash mismatch" → pesan tak sampai walau no-error).
		_, _ = client.GetGroupInfo(sendCtx, g.JID)
		_, err := client.SendMessage(sendCtx, g.JID, &waProto.Message{
			Conversation: proto.String(text),
		})
		c()
		if err != nil {
			fmt.Printf("[BC] ❌ gagal kirim ke %s: %v\n", g.JID, err)
			continue
		}
		sent++
		time.Sleep(700 * time.Millisecond)
	}
	fmt.Printf("[BC] ✅ selesai: %d/%d grup.\n", sent, len(groups))
	return sent, len(groups)
}
