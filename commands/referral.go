package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// REFERRAL BROADCAST — kirim referral ke semua grup tiap N jam.
// Member pertama yang me-reply pesan referral mendapat reward balance.
// =================================================================

// ReferralData adalah state yang dilekatkan ke reply-router untuk tiap pesan referral.
type ReferralData struct {
	Code    string
	Reward  float64
	GroupID string
}

var referralResetChan = make(chan struct{}, 1)

func init() {
	RegisterCommand(Command{
		Name:        "Referral Sekarang",
		Category:    "Owner",
		Aliases:     []string{"refnow"},
		Pattern:     regexp.MustCompile(`(?i)^\s*refnow\s*$`),
		Description: "Broadcast referral ke semua grup sekarang (owner)",
		Execute:     ExecuteRefNow,
	}).Use(OwnerOnlyMiddleware)

	RegisterCommand(Command{
		Name:        "Set Interval Referral",
		Category:    "Owner",
		Aliases:     []string{"setrefinterval"},
		Pattern:     regexp.MustCompile(`(?i)^\s*setrefinterval\s+(\d+)\s*$`),
		Description: "Ubah interval broadcast referral (jam) — owner",
		Execute:     ExecuteSetRefInterval,
	}).Use(OwnerOnlyMiddleware)
}

// generateReferralCode membuat kode unik singkat.
func generateReferralCode() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "REF-" + strings.ToUpper(hex.EncodeToString(b))
}

// BroadcastReferral mengirim satu pesan referral ke semua grup yang diikuti bot
// dan mendaftarkan tiap pesan ke reply-router agar bisa diklaim.
func BroadcastReferral(client *whatsmeow.Client) (int, error) {
	code := generateReferralCode()
	reward := src.AppConfig.ReferralReward
	if reward <= 0 {
		reward = 0.5
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		return 0, err
	}

	text := fmt.Sprintf(
		"🎁 *REFERRAL HADIAH!*\n\n"+
			"Reply pesan ini *paling cepat* untuk klaim *$%.3f*!\n"+
			"Hanya *1 orang tercepat* per grup yang dapat. 🏆\n\n"+
			"_Kode: %s_", reward, code)

	sent := 0
	for _, g := range groups {
		groupJID := g.JID
		groupID := groupJID.ToNonAD().String()

		msgID := src.NewMessageID(client)
		_, err := client.SendMessage(ctx, groupJID, &waProto.Message{
			Conversation: proto.String(text),
		}, whatsmeow.SendRequestExtra{ID: msgID})
		if err != nil {
			continue
		}

		replyRouter.Register(string(msgID), "referral", &ReferralData{
			Code:    code,
			Reward:  reward,
			GroupID: groupID,
		})
		sent++

		// Jeda kecil agar tidak kena rate-limit
		time.Sleep(700 * time.Millisecond)
	}

	fmt.Printf("[REFERRAL] Broadcast kode %s ke %d grup.\n", code, sent)
	return sent, nil
}

// StartReferralScheduler menjalankan broadcast referral berkala sesuai interval config.
func StartReferralScheduler(client *whatsmeow.Client) {
	go func() {
		for {
			hours := src.AppConfig.ReferralIntervalHours
			if hours <= 0 {
				hours = 12
			}
			timer := time.NewTimer(time.Duration(hours) * time.Hour)

			select {
			case <-timer.C:
				if _, err := BroadcastReferral(client); err != nil {
					fmt.Printf("[REFERRAL] Gagal broadcast: %v\n", err)
				}
			case <-referralResetChan:
				// Interval diubah owner; hentikan timer lama lalu mulai ulang loop
				if !timer.Stop() {
					<-timer.C
				}
			}
		}
	}()
	fmt.Printf("[SCHEDULER] ✅ Referral broadcast aktif (tiap %d jam).\n", src.AppConfig.ReferralIntervalHours)
}

// handleReferralClaim menangani reply user ke pesan referral.
func handleReferralClaim(ctx *ContextBot, rc *ReplyContext) error {
	data, ok := rc.Data.(*ReferralData)
	if !ok {
		return nil
	}

	// Tentukan ID user (samakan dengan logika handler: utamakan SenderAlt)
	userJID := ctx.SenderJID.String()
	if !ctx.SenderAlt.IsEmpty() {
		userJID = ctx.SenderAlt.String()
	}

	won, newBal := src.DB.ClaimReferral(data.Code, data.GroupID, userJID, data.Reward)
	if won {
		return ctx.Reply(fmt.Sprintf(
			"🎉 *Selamat %s!*\nKamu klaim referral *tercepat*! 🏆\n\n💰 +$%.3f\nSaldo sekarang: *$%.3f*",
			ctx.PushName, data.Reward, newBal))
	}
	return ctx.Reply("😢 Yah, referral ini sudah diklaim member lain yang lebih cepat.")
}

// ExecuteRefNow memicu broadcast referral manual (owner).
func ExecuteRefNow(ctx *ContextBot) error {
	_ = ctx.React("⏳")
	sent, err := BroadcastReferral(ctx.Client)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Gagal broadcast referral: %v", err))
	}
	return ctx.Reply(fmt.Sprintf("✅ Referral dikirim ke *%d* grup.", sent))
}

// ExecuteSetRefInterval mengubah interval broadcast referral (owner).
func ExecuteSetRefInterval(ctx *ContextBot) error {
	hours, err := strconv.Atoi(strings.TrimSpace(ctx.Args))
	if err != nil || hours <= 0 {
		return ctx.Reply("⚠️ Format salah. Contoh: `setrefinterval 12`")
	}

	src.AppConfig.ReferralIntervalHours = hours
	if err := src.SaveConfig(); err != nil {
		return ctx.Reply(fmt.Sprintf("⚠️ Interval diubah tapi gagal disimpan: %v", err))
	}

	// Restart timer scheduler
	select {
	case referralResetChan <- struct{}{}:
	default:
	}

	return ctx.Reply(fmt.Sprintf("✅ Interval referral diubah menjadi *%d jam*.", hours))
}
