package commands

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// ANTI-BOT CERDAS — deteksi bot lain via format message-ID + flood,
// lalu beri OTP challenge 1×; gagal/timeout → kick.
// =================================================================

const (
	otpTTL       = 60 * time.Second
	floodWindow  = 7 * time.Second
	floodMax     = 6 // >= 6 pesan dalam floodWindow = flood
	adminCacheTL = 5 * time.Minute
)

// ---- State in-memory ----

type otpState struct {
	code      string
	expiresAt time.Time
	sender    types.JID
	triggerID types.MessageID
}

var (
	otpStore = make(map[string]*otpState)
	otpMu    sync.Mutex

	floodStore = make(map[string][]time.Time)
	floodMu    sync.Mutex

	adminCache   = make(map[string]adminCacheEntry)
	adminCacheMu sync.Mutex
)

type adminCacheEntry struct {
	users map[string]bool
	exp   time.Time
}

func init() {
	RegisterCommand(Command{
		Name:        "Anti-Bot",
		Category:    "Group",
		Aliases:     []string{"antibot"},
		Pattern:     regexp.MustCompile(`(?i)^\s*antibot\s+(on|off)\s*$`),
		Description: "Aktifkan/matikan proteksi anti-bot grup (admin)",
		Execute:     ExecuteAntibotToggle,
	}).Use(GroupOnlyMiddleware)
}

// ====================== PIPELINE (dipanggil dari handler) ======================

// HandleAntibot menjalankan deteksi & OTP. Mengembalikan true bila pesan
// dikonsumsi (jangan diproses lagi sebagai command/reply).
func HandleAntibot(ctx *ContextBot) bool {
	if !ctx.IsGroup || ctx.IsOwner {
		return false
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	if !src.DB.IsGroupAntibot(groupID) {
		return false
	}

	su := ctx.SenderJID.ToNonAD().User
	sl := ctx.SenderAlt.ToNonAD().User
	key := groupID + "|" + su + "|" + sl

	// 1. Sudah trusted? lewati
	if (su != "" && src.DB.IsTrusted(groupID, su)) || (sl != "" && src.DB.IsTrusted(groupID, sl)) {
		return false
	}
	// 2. Admin? lewati (cached)
	if isCachedAdmin(ctx, groupID) {
		return false
	}

	// 3. Ada OTP tertunda → verifikasi
	otpMu.Lock()
	st, pending := otpStore[key]
	otpMu.Unlock()
	if pending {
		if strings.TrimSpace(ctx.TextMessage) == st.code {
			otpMu.Lock()
			delete(otpStore, key)
			otpMu.Unlock()
			if su != "" {
				src.DB.AddTrust(groupID, su)
			}
			if sl != "" {
				src.DB.AddTrust(groupID, sl)
			}
			_ = ctx.Reply("✅ Verifikasi berhasil. Selamat datang! Kamu sekarang terpercaya.")
			return true
		}
		// Pesan lain selama OTP aktif → diabaikan (timer yang urus kick)
		return true
	}

	// 4. Deteksi sinyal bot
	suspicious := false
	reason := ""
	if src.LooksLikeBotMessageID(string(ctx.Msg.Info.ID)) {
		suspicious = true
		reason = "format ID pesan menyerupai bot"
	} else if isFlooding(key) {
		suspicious = true
		reason = "mengirim pesan terlalu cepat (flood)"
	}
	if !suspicious {
		return false
	}

	// 5. Buat OTP challenge (1×) + jadwalkan kick saat timeout
	code := genOTP()
	otpMu.Lock()
	otpStore[key] = &otpState{
		code:      code,
		expiresAt: time.Now().Add(otpTTL),
		sender:    ctx.SenderJID,
		triggerID: ctx.Msg.Info.ID,
	}
	otpMu.Unlock()

	_ = ctx.Reply(fmt.Sprintf(
		"🤖 *VERIFIKASI ANTI-BOT*\n\nTerdeteksi: _%s_\nKetik kode berikut dalam %d detik untuk membuktikan kamu manusia:\n\n*%s*\n\n_Gagal/terlambat = dikeluarkan otomatis._",
		reason, int(otpTTL.Seconds()), code))

	go scheduleKick(ctx, groupID, key)
	return true
}

func scheduleKick(ctx *ContextBot, groupID, key string) {
	time.Sleep(otpTTL)

	otpMu.Lock()
	st, stillPending := otpStore[key]
	if stillPending {
		delete(otpStore, key)
	}
	otpMu.Unlock()
	if !stillPending {
		return // sudah terverifikasi
	}

	// Hapus pesan pemicu (best-effort) lalu kick
	revoke := ctx.Client.BuildRevoke(ctx.ChatJID, st.sender, st.triggerID)
	_, _ = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, revoke)

	_, err := ctx.Client.UpdateGroupParticipants(context.Background(), ctx.ChatJID,
		[]types.JID{st.sender.ToNonAD()}, "remove")
	if err != nil {
		_ = ctx.Reply("⚠️ Verifikasi gagal, tapi bot bukan admin jadi tidak bisa mengeluarkan user.")
		return
	}
	_ = ctx.Reply("🚫 User dikeluarkan: gagal verifikasi anti-bot.")
}

// ====================== HELPERS ======================

func genOTP() string {
	b := make([]byte, 2)
	rand.Read(b)
	n := (int(b[0])<<8 | int(b[1])) % 10000
	return fmt.Sprintf("%04d", n)
}

func isFlooding(key string) bool {
	now := time.Now()
	floodMu.Lock()
	defer floodMu.Unlock()

	var recent []time.Time
	for _, t := range floodStore[key] {
		if now.Sub(t) < floodWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	floodStore[key] = recent
	return len(recent) >= floodMax
}

func isCachedAdmin(ctx *ContextBot, groupID string) bool {
	adminCacheMu.Lock()
	e, ok := adminCache[groupID]
	adminCacheMu.Unlock()

	if !ok || time.Now().After(e.exp) {
		e = adminCacheEntry{users: make(map[string]bool), exp: time.Now().Add(adminCacheTL)}
		if info, err := ctx.Client.GetGroupInfo(context.Background(), ctx.ChatJID); err == nil {
			for _, p := range info.Participants {
				if p.IsAdmin || p.IsSuperAdmin {
					if u := p.JID.ToNonAD().User; u != "" {
						e.users[u] = true
					}
					if u := p.LID.ToNonAD().User; u != "" {
						e.users[u] = true
					}
				}
			}
		}
		adminCacheMu.Lock()
		adminCache[groupID] = e
		adminCacheMu.Unlock()
	}

	su := ctx.SenderJID.ToNonAD().User
	sl := ctx.SenderAlt.ToNonAD().User
	return (su != "" && e.users[su]) || (sl != "" && e.users[sl])
}

// ====================== COMMAND: TOGGLE ======================

func ExecuteAntibotToggle(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur anti-bot.")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	on := strings.Contains(strings.ToLower(ctx.TextMessage), "on")
	src.DB.SetGroupAntibot(groupID, on)
	if on {
		return ctx.Reply("🛡️ *Anti-Bot AKTIF*. Bot lain & flood akan diverifikasi (OTP) lalu dikeluarkan bila gagal.\n_Pastikan bot adalah admin agar bisa mengeluarkan._")
	}
	return ctx.Reply("🛡️ *Anti-Bot NONAKTIF*.")
}
