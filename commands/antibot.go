package commands

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// ANTI-BOT CERDAS — deteksi berbasis SKOR (gabungan sinyal):
//   1) ID khas Baileys (3EB0/BAE5)               → +3  (HINT, tak final)
//   2) Flood (banyak pesan dalam waktu singkat)  → +4
//   3) Teks SAMA berulang (>=3x)                 → +4
//   4) Kirim TANPA "mengetik" (presence aktif)   → +4
// Skor >= antibotThreshold → CAPTCHA.
//
// REALITA (jujur): bot yang pakai ID normal + MEMALSUKAN "mengetik" + tidak
// spam HAMPIR mustahil dibedakan dari manusia lewat sinyal pasif. Maka:
//   - Sinyal di atas menangkap bot "biasa" (tak mengetik / spam / ID Baileys).
//   - CAPTCHA adalah penentu akhir: manusia bisa jawab kode → aman & jadi
//     trusted; bot tak bisa baca kode → ke-kick. Jadi false-positive pun aman.
//   - Format "AC"+hex TIDAK dipakai sebagai sinyal (itu format WA asli/iOS).
//
// CAPTCHA: pesan BERIKUTNYA harus = kode. Bukan kode → LANGSUNG kick.
// Tidak menjawab dalam captchaTTL → kick (timeout).
//
// Semua state in-memory + cache → tidak memperlambat respon bot.
// =================================================================

const (
	antibotThreshold = 4
	captchaTTL       = 60 * time.Second
	floodWindow      = 7 * time.Second
	floodMax         = 6 // >= 6 pesan dalam floodWindow = flood
	adminCacheTL     = 5 * time.Minute
	typingWindow     = 40 * time.Second

	scoreBotID   = 3 // hint: ID khas Baileys (3EB0/BAE5). Sendiri < ambang → butuh korroborasi
	scoreFlood   = 4 // banyak pesan dalam waktu singkat
	scoreRepeat  = 4 // teks SAMA dikirim berulang (>=repeatMax)
	scoreNoTyped = 4 // kirim TANPA "mengetik" (presence aktif) — pembeda utama bot sederhana

	repeatWindow = 60 * time.Second
	repeatMax    = 3 // teks sama >=3x dalam repeatWindow = berulang
)

// ---- State in-memory ----

type captchaState struct {
	code      string
	expiresAt time.Time
	sender    types.JID
	triggerID types.MessageID
}

var (
	captchaStore = make(map[string]*captchaState)
	captchaMu    sync.Mutex

	floodStore = make(map[string][]time.Time)
	floodMu    sync.Mutex

	repeatStore = make(map[string]*repeatRec)
	repeatMu    sync.Mutex

	adminCache   = make(map[string]adminCacheEntry)
	adminCacheMu sync.Mutex

	// Pelacak "mengetik" (presence). Bot biasanya kirim TANPA composing.
	typingStore  = make(map[string]time.Time)
	typingMu     sync.Mutex
	presenceSeen int32 // 0/1 — apakah event presence pernah diterima sama sekali
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

// ====================== TYPING TRACKER (dipanggil dari main eventHandler) ======================

// RecordTyping mencatat event "mengetik" (composing) dari seorang user di sebuah chat.
func RecordTyping(chat, sender types.JID) {
	atomic.StoreInt32(&presenceSeen, 1)
	key := chat.ToNonAD().String() + "|" + sender.ToNonAD().User
	typingMu.Lock()
	typingStore[key] = time.Now()
	typingMu.Unlock()
}

func typedRecently(groupID, user string) bool {
	if user == "" {
		return false
	}
	typingMu.Lock()
	t, ok := typingStore[groupID+"|"+user]
	typingMu.Unlock()
	return ok && time.Since(t) < typingWindow
}

func presenceActive() bool { return atomic.LoadInt32(&presenceSeen) == 1 }

// ====================== PIPELINE (dipanggil dari handler) ======================

// HandleAntibot menjalankan deteksi skor & captcha. Mengembalikan true bila pesan
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

	// Trusted / admin → lewati
	if (su != "" && src.DB.IsTrusted(groupID, su)) || (sl != "" && src.DB.IsTrusted(groupID, sl)) {
		return false
	}
	if isCachedAdmin(ctx, groupID) {
		return false
	}

	// === Ada captcha tertunda? pesan BERIKUTNYA menentukan nasib ===
	captchaMu.Lock()
	st, pending := captchaStore[key]
	captchaMu.Unlock()
	if pending {
		if strings.TrimSpace(ctx.TextMessage) == st.code {
			captchaMu.Lock()
			delete(captchaStore, key)
			captchaMu.Unlock()
			if su != "" {
				src.DB.AddTrust(groupID, su)
			}
			if sl != "" {
				src.DB.AddTrust(groupID, sl)
			}
			_ = ctx.Reply("✅ Verifikasi berhasil. Kamu sekarang terpercaya.")
			return true
		}
		// Pesan kedua BUKAN kode → langsung kick
		captchaMu.Lock()
		delete(captchaStore, key)
		captchaMu.Unlock()
		go kickUser(ctx, st.sender, st.triggerID, "jawaban captcha salah")
		return true
	}

	// === Hitung skor ===
	score := 0
	var reasons []string
	if src.LooksLikeBotMessageID(string(ctx.Msg.Info.ID)) {
		score += scoreBotID
		reasons = append(reasons, "ID khas bot")
	}
	if isFlooding(key) {
		score += scoreFlood
		reasons = append(reasons, "flood")
	}
	if isRepeating(key, ctx.TextMessage) {
		score += scoreRepeat
		reasons = append(reasons, "teks berulang")
	}
	// Sinyal "tanpa mengetik" hanya dipakai bila presence memang diterima
	// (hindari false-positive saat presence tidak tersedia di environment).
	if presenceActive() && !typedRecently(groupID, su) && !typedRecently(groupID, sl) {
		score += scoreNoTyped
		reasons = append(reasons, "kirim tanpa mengetik")
	}

	// DEBUG: tampilkan ID & skor tiap pesan di grup ber-antibot (untuk tuning).
	fmt.Printf("[ANTIBOT] grup=%s pengirim=%s id=%q skor=%d/%d alasan=%v presence=%v\n",
		groupID, su, string(ctx.Msg.Info.ID), score, antibotThreshold, reasons, presenceActive())

	if score < antibotThreshold {
		return false
	}

	// === Lolos ambang → terbitkan CAPTCHA ===
	code := genCode()
	captchaMu.Lock()
	captchaStore[key] = &captchaState{
		code:      code,
		expiresAt: time.Now().Add(captchaTTL),
		sender:    ctx.SenderJID,
		triggerID: ctx.Msg.Info.ID,
	}
	captchaMu.Unlock()

	_ = ctx.Reply(fmt.Sprintf(
		"`VERIFIKASI ANTI-BOT`\n\n*Terdeteksi*: _%s_\n\nBalas pesan apa pun dengan kode ini:\n\n        ➡️  *%s*  ⬅️\n\n_Salah / diam %d detik = dikeluarkan._",
		strings.Join(reasons, ", "), code, int(captchaTTL.Seconds())))

	go scheduleCaptchaTimeout(ctx, key)
	return true
}

func scheduleCaptchaTimeout(ctx *ContextBot, key string) {
	time.Sleep(captchaTTL)

	captchaMu.Lock()
	st, stillPending := captchaStore[key]
	if stillPending {
		delete(captchaStore, key)
	}
	captchaMu.Unlock()
	if !stillPending {
		return // sudah terverifikasi / sudah di-kick
	}
	kickUser(ctx, st.sender, st.triggerID, "tidak menjawab captcha")
}

// kickUser menghapus pesan pemicu (best-effort) lalu mengeluarkan user.
func kickUser(ctx *ContextBot, sender types.JID, triggerID types.MessageID, reason string) {
	revoke := ctx.Client.BuildRevoke(ctx.ChatJID, sender, triggerID)
	_, _ = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, revoke)

	_, err := ctx.Client.UpdateGroupParticipants(context.Background(), ctx.ChatJID,
		[]types.JID{sender.ToNonAD()}, "remove")
	if err != nil {
		_ = ctx.Reply("⚠️ Gagal verifikasi, tapi bot bukan admin jadi tidak bisa mengeluarkan user.")
		return
	}
	_ = ctx.Reply(fmt.Sprintf("🚫 User dikeluarkan (%s).", reason))
}

// ====================== HELPERS ======================

func genCode() string {
	b := make([]byte, 2)
	rand.Read(b)
	n := (int(b[0])<<8 | int(b[1])) % 10000
	return fmt.Sprintf("%04d", n)
}

// repeatRec melacak pengulangan teks yang sama dari satu pengirim.
type repeatRec struct {
	text  string
	count int
	last  time.Time
}

// isRepeating true bila teks SAMA dikirim berulang (>=repeatMax) dalam repeatWindow.
func isRepeating(key, text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	now := time.Now()

	repeatMu.Lock()
	defer repeatMu.Unlock()

	r := repeatStore[key]
	if r == nil || now.Sub(r.last) > repeatWindow || r.text != t {
		repeatStore[key] = &repeatRec{text: t, count: 1, last: now}
		return false
	}
	r.count++
	r.last = now
	return r.count >= repeatMax
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
		return ctx.Reply("🛡️ *Anti-Bot AKTIF*.")
	}
	return ctx.Reply("🛡️ *Anti-Bot NONAKTIF*.")
}
