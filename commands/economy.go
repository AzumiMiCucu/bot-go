package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// FITUR EKONOMI: balance, profile, transfer, leaderboard
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Cek Saldo",
		Category:    "Ekonomi",
		Aliases:     []string{"balance", "saldo", "bal"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(balance|saldo|bal)\s*$`),
		Description: "Mengecek saldo balance kamu",
		Execute:     ExecuteBalance,
	})

	RegisterCommand(Command{
		Name:        "Profil Saya",
		Category:    "Ekonomi",
		Aliases:     []string{"me", "profil", "profile"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(me|profil|profile)\s*$`),
		Description: "Menampilkan kartu profil & statistik kamu",
		Execute:     ExecuteProfile,
	})

	RegisterCommand(Command{
		Name:        "Transfer Saldo",
		Category:    "Ekonomi",
		Aliases:     []string{"transfer", "tf"},
		Pattern:     regexp.MustCompile(`(?i)^(?:transfer|tf)\s+([\d.]+)`),
		Description: "Transfer balance ke user lain (reply/tag)",
		Execute:     ExecuteTransfer,
	})

	RegisterCommand(Command{
		Name:        "Leaderboard Saldo",
		Category:    "Ekonomi",
		Aliases:     []string{"leaderboard", "lb", "rich"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(leaderboard|lb|rich)\s*$`),
		Description: "Peringkat user dengan saldo terbanyak",
		Execute:     ExecuteLeaderboard,
	})
}

// dbUserID mengembalikan ID user yang dipakai di database (utamakan SenderAlt).
func dbUserID(ctx *ContextBot) string {
	if !ctx.SenderAlt.IsEmpty() {
		return ctx.SenderAlt.String()
	}
	return ctx.SenderJID.String()
}

func ExecuteBalance(ctx *ContextBot) error {
	return ctx.Reply(fmt.Sprintf(
		"💳 *SALDO KAMU*\n\n👤 %s\n💰 Balance: *$%.3f*",
		ctx.PushName, ctx.UserBalance))
}

func ExecuteProfile(ctx *ContextBot) error {
	uid := dbUserID(ctx)
	user := src.DB.GetUser(uid)
	if user == nil {
		return ctx.Reply("⚠️ Data profil belum tersedia. Coba kirim pesan dulu.")
	}
	rank := src.DB.GetBalanceRank(uid)

	rankStr := "-"
	if rank > 0 {
		rankStr = fmt.Sprintf("#%d", rank)
	}

	tier := user.Tier
	if tier == "" {
		tier = "free"
	}

	return ctx.Reply(fmt.Sprintf(
		"🪪 *PROFIL*\n\n"+
			"👤 *Nama:* %s\n"+
			"💰 *Saldo:* $%.3f\n"+
			"🏆 *Ranking:* %s\n"+
			"🎖️ *Tier:* %s\n"+
			"💬 *Total Pesan:* %d\n"+
			"📅 *Bergabung:* %s",
		user.Name, user.Balance, rankStr, tier,
		user.MessageCount, user.RegisteredAt.Format("02 Jan 2006")))
}

func ExecuteTransfer(ctx *ContextBot) error {
	// Ambil jumlah dari argumen pertama
	fields := strings.Fields(ctx.Args)
	if len(fields) == 0 {
		return ctx.Reply("⚠️ Format: `transfer <jumlah>` sambil reply/tag user tujuan.")
	}
	amount, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || amount <= 0 {
		return ctx.Reply("⚠️ Jumlah tidak valid. Contoh: `transfer 0.5` (reply user tujuan).")
	}

	target := resolveTargetJID(ctx)
	if target == "" {
		return ctx.Reply("⚠️ Tidak ada tujuan. Reply pesan atau tag user yang ingin ditransfer.")
	}

	from := dbUserID(ctx)
	if target == from {
		return ctx.Reply("⚠️ Tidak bisa transfer ke diri sendiri.")
	}

	ok, err := src.DB.TransferBalance(from, target, amount)
	if !ok {
		msg := "❌ Transfer gagal."
		if err != nil {
			msg = "❌ " + err.Error()
		}
		return ctx.Reply(msg)
	}

	targetNum := strings.Split(target, "@")[0]
	return ctx.Reply(fmt.Sprintf(
		"✅ *TRANSFER BERHASIL*\n\n💸 $%.3f → @%s\n💰 Sisa saldo kamu: *$%.3f*",
		amount, targetNum, ctx.UserBalance-amount))
}

func ExecuteLeaderboard(ctx *ContextBot) error {
	top := src.DB.GetTopBalance(10)
	if len(top) == 0 {
		return ctx.Reply("📊 Belum ada data leaderboard.")
	}

	medals := []string{"🥇", "🥈", "🥉"}
	var sb strings.Builder
	sb.WriteString("🏆 *LEADERBOARD SALDO*\n\n")
	for i, u := range top {
		prefix := fmt.Sprintf("%d.", i+1)
		if i < len(medals) {
			prefix = medals[i]
		}
		name := u.Name
		if name == "" {
			name = strings.Split(u.ID, "@")[0]
		}
		sb.WriteString(fmt.Sprintf("%s *%s* — $%.3f\n", prefix, name, u.Balance))
	}
	return ctx.Reply(sb.String())
}

// resolveTargetJID mengambil JID tujuan dari mention atau pesan yang di-reply.
func resolveTargetJID(ctx *ContextBot) string {
	ext := ctx.Msg.Message.GetExtendedTextMessage()
	if ext == nil {
		return ""
	}
	ctxInfo := ext.GetContextInfo()
	if ctxInfo == nil {
		return ""
	}

	// 1. Mention (@user)
	for _, m := range ctxInfo.GetMentionedJID() {
		if parsed, err := types.ParseJID(m); err == nil {
			return parsed.String()
		}
	}

	// 2. Reply → participant pesan yang di-quote
	if participant := ctxInfo.GetParticipant(); participant != "" {
		if parsed, err := types.ParseJID(participant); err == nil {
			return parsed.String()
		}
	}

	return ""
}
