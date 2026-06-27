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

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// ANTI-BOT CERDAS — GATE device-index lalu SKOR perilaku:
//
// LANGKAH 1 — primary vs secondary (device-index JID, BUKAN pn/lid):
//   • device 0 (HP UTAMA)  → langsung PASS manusia, cache 1 jam, dicek ulang
//                            setelahnya. Baileys/whatsmeow TAK BISA jadi device 0.
//   • device != 0 (COMPANION: WA Web/Desktop/bot) → lanjut cek perilaku ↓
//
// LANGKAH 2 — skor perilaku (hanya untuk companion):
//   1) ID khas Baileys / verdict bot definitif   → langsung tembus ambang
//   2) Flood (banyak pesan dalam waktu singkat)  → +4
//   3) Teks SAMA berulang (>=3x)                 → +4
//   4) Kirim TANPA "mengetik" (presence aktif)   → +4
//   5) Spam command berawalan prefix bot         → +4
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
	typingTTL        = 1 * time.Hour // "terbukti manusia" hanya berlaku 1 jam sejak ketikan terakhir
	humanTTL         = 1 * time.Hour // HP utama (device 0) = manusia, berlaku 1 jam lalu dicek ulang

	scoreUnknownID = 4 // verdict suspect (mis. ID device tak dikenal, metadata tak lengkap)
	scoreFlood     = 4 // banyak pesan dalam waktu singkat
	scoreRepeat    = 4 // teks SAMA dikirim berulang (>=repeatMax)
	scoreNoTyped   = 4 // kirim TANPA "mengetik" (presence aktif) — pembeda utama bot sederhana

	repeatWindow = 60 * time.Second
	repeatMax    = 3 // teks sama >=3x dalam repeatWindow = berulang

	scorePrefix  = 4 // spam command berawalan prefix bot (.menu #owner /start) berulang
	prefixWindow = 60 * time.Second
	prefixMax    = 3 // >=3 pesan command-prefix dalam prefixWindow = spam command
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

	// Pelacak pesan berawalan prefix-command (.menu/#owner/dst) per pengirim.
	prefixStore = make(map[string][]time.Time)
	prefixMu    sync.Mutex

	adminCache   = make(map[string]adminCacheEntry)
	adminCacheMu sync.Mutex

	// Pelacak "mengetik" (presence). Bot biasanya kirim TANPA composing.
	typingStore  = make(map[string]time.Time)
	typingMu     sync.Mutex
	presenceSeen int32 // 0/1 — apakah event presence pernah diterima sama sekali

	// Cache "terbukti manusia" untuk HP utama (device-index 0). Sekali pesan datang
	// dari device 0, pengirim di-pass sebagai manusia & disimpan humanTTL (1 jam);
	// dalam jendela itu pesannya tak discan lagi. Setelah 1 jam → dicek ulang.
	humanStore = make(map[string]time.Time)
	humanMu    sync.Mutex
)

type adminCacheEntry struct {
	users    map[string]bool
	botAdmin bool // apakah BOT sendiri admin di grup ini
	exp      time.Time
}

func init() {
	RegisterCommand(Command{
		Name:        "Anti-Bot",
		Category:    "Group",
		Aliases:     []string{"antibot"},
		Pattern:     regexp.MustCompile(`(?i)^\s*antibot(?:\s+(.+))?\s*$`),
		Description: "Proteksi anti-bot grup: on/off/status/help (admin)",
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

// typedLately: apakah user ini mengetik dalam typingTTL terakhir (default 1 jam).
// Kalau ya → "terbukti manusia" sementara → kebal sinyal typing (forward/media/paste
// aman). Status ini KEDALUWARSA setelah 1 jam tanpa ketikan, supaya bot yang cuma
// sesekali memicu fitur ber-typing tidak kebal selamanya.
func typedLately(groupID, user string) bool {
	if user == "" {
		return false
	}
	typingMu.Lock()
	t, ok := typingStore[groupID+"|"+user]
	typingMu.Unlock()
	return ok && time.Since(t) < typingTTL
}

func presenceActive() bool { return atomic.LoadInt32(&presenceSeen) == 1 }

// markProvenHuman menandai pengirim "terbukti manusia" (datang dari HP utama,
// device 0) selama humanTTL. Refresh tiap pesan device-0 berikutnya.
func markProvenHuman(key string) {
	humanMu.Lock()
	humanStore[key] = time.Now()
	humanMu.Unlock()
}

// provenHumanLately: apakah pengirim ini sudah terbukti manusia dalam humanTTL
// (1 jam) terakhir. Bila ya → antibot melewatinya tanpa scan ulang.
func provenHumanLately(key string) bool {
	humanMu.Lock()
	t, ok := humanStore[key]
	humanMu.Unlock()
	return ok && time.Since(t) < humanTTL
}

// isMediaMsg true bila pesan berupa media (foto/video/dokumen/audio/stiker).
func isMediaMsg(m *waProto.Message) bool {
	return m.GetImageMessage() != nil || m.GetVideoMessage() != nil ||
		m.GetDocumentMessage() != nil || m.GetAudioMessage() != nil ||
		m.GetStickerMessage() != nil
}

// isForwardedMsg true bila pesan adalah hasil FORWARD (tidak butuh mengetik).
func isForwardedMsg(m *waProto.Message) bool {
	var ci *waProto.ContextInfo
	switch {
	case m.GetExtendedTextMessage() != nil:
		ci = m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		ci = m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		ci = m.GetVideoMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		ci = m.GetDocumentMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		ci = m.GetAudioMessage().GetContextInfo()
	}
	if ci == nil {
		return false
	}
	return ci.GetIsForwarded() || ci.GetForwardingScore() > 0
}

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

	// Antibot hanya berguna bila BOT admin (kalau tidak, captcha & kick mustahil).
	// Bot bukan admin → diam total: tidak scan, tidak kirim apa pun.
	if !botIsGroupAdmin(ctx, groupID) {
		return false
	}

	su := ctx.SenderJID.ToNonAD().User
	sl := ctx.SenderAlt.ToNonAD().User
	key := groupID + "|" + su + "|" + sl

	// Trusted / admin → lewati
	if groupModExempt(ctx, groupID) {
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

	// === Manusia terbukti (HP utama) dalam 1 jam terakhir → lewati tanpa scan ===
	if provenHumanLately(key) {
		return false
	}

	// Voice note / audio = bukti POSITIF manusia (bot hampir tak pernah kirim VN).
	// Diperlakukan seperti "mengetik": menandai user kebal sinyal typing (1 jam).
	// Catatan: "tak pernah VN" TIDAK dipakai sebagai tuduhan tunggal (terlalu lemah);
	// VN hanya dipakai satu arah sebagai bukti manusia.
	if ctx.Msg.Message.GetAudioMessage() != nil {
		RecordTyping(ctx.ChatJID, ctx.SenderJID)
	}

	// === Analisis komprehensif pesan (ID, raw metadata, device-index, bot signals) ===
	det := src.DetectBot(ctx.Msg)

	// === GATE PRIMARY / SECONDARY (device-index JID) ===
	// HP UTAMA (device 0): registrasi nomor langsung — Baileys/whatsmeow TAK BISA
	// jadi device 0 (mereka pairing sebagai companion). Jadi pesan device-0 = manusia:
	// langsung pass, simpan "terbukti manusia" 1 jam, dicek ulang setelahnya.
	// Pengecualian: marker bot DEFINITIF (server/akun bot WA resmi) tetap menang.
	if det.IsPrimary && det.Verdict != src.VerdictBot {
		markProvenHuman(key)
		fmt.Printf("[ANTIBOT] grup=%s pengirim=%s device=0(primary) → PASS manusia (cache 1 jam)\n", groupID, su)
		return false
	}

	// COMPANION (device != 0 — WA Web/Desktop/bot): jalankan cek perilaku di bawah.
	// Manusia di WA Web/Desktop akan lolos (mengetik, tak flood, tak spam command);
	// hanya bot companion (tak mengetik / flood / spam / ID Baileys) yang tembus ambang.

	score := 0
	var reasons []string
	dev := det.DeviceClass

	// --- Verdict deteksi (satu sumber kebenaran, berpusat DeviceListMetadata) ---
	// Bot/Baileys definitif → langsung tembus ambang (tak perlu sinyal perilaku).
	// Suspect → skor sedang, butuh sinyal perilaku untuk tembus.
	// Human/Unknown → TIDAK dihukum sinyal device; hanya flood/repeat murni berlaku
	// (mencegah false-positive HP asli yang ber-addressing lid).
	switch det.Verdict {
	case src.VerdictBot, src.VerdictBaileys:
		score += antibotThreshold // langsung lewat ambang
		reasons = append(reasons, det.VerdictReason)
	case src.VerdictSuspect:
		score += scoreUnknownID
		reasons = append(reasons, det.VerdictReason)
	}

	if isFlooding(key) {
		score += scoreFlood
		reasons = append(reasons, "flood")
	}
	if isRepeating(key, ctx.TextMessage) {
		score += scoreRepeat
		reasons = append(reasons, "teks berulang")
	}
	// Sinyal TYPING canggih: hukum HANYA bila user "tak pernah terlihat mengetik"
	// di grup ini, DAN pesan ini wajar perlu diketik (teks biasa, bukan media,
	// bukan forward). Jadi:
	//   - Manusia yang mengetik dalam 1 jam terakhir → kebal (forward/media/paste aman).
	//   - Media + caption  → dikecualikan (WA sering tak kirim composing di media).
	//   - Pesan forward    → dikecualikan (memang tak diketik).
	//   - Bot yang tak mengetik (atau ketikan terakhirnya >1 jam lalu) & kirim teks → kena.
	hasText := strings.TrimSpace(ctx.TextMessage) != ""
	isPlainText := hasText && !isMediaMsg(ctx.Msg.Message)
	isForwarded := isForwardedMsg(ctx.Msg.Message)
	if isPlainText && !isForwarded && presenceActive() &&
		!typedLately(groupID, su) && !typedLately(groupID, sl) {
		score += scoreNoTyped
		reasons = append(reasons, "tak mengetik (>1 jam)")
	}
	// Spam command bot: pesan berawalan prefix command (.menu/#owner/dst) BERULANG
	// (>=prefixMax dalam prefixWindow). Menangkap bot yang membombardir command
	// BERAGAM — luput dari isRepeating yang hanya cek teks IDENTIK. Satu panggilan
	// .menu manusia biasa tak cukup (butuh pengulangan) → false-positive minim.
	if isBotCommandPrefix(ctx.TextMessage) && isPrefixSpamming(key) {
		score += scorePrefix
		reasons = append(reasons, "spam command bot")
	}

	// DEBUG: tampilkan verdict, engine checks (TK/DV/PF/ID) & skor tiap pesan di
	// grup ber-antibot (untuk tuning). Engine checks ikut dihitung di DetectBot.
	// SIDIK JARI STRUKTURAL ikut dicetak (fase DUMP): bandingkan baris bot vs HP asli
	// untuk menemukan pembeda struktural sebelum aturan vonis dibuat — scoring di sini
	// SENGAJA belum memakai struktur agar tak menambah false-positive.
	sf := det.Struct
	fmt.Printf("[ANTIBOT] grup=%s pengirim=%s id=%q dev=%s deviceIdx=%d(companion) verdict=%s(baileysScore=%d) engine=[TK:%s DV:%s PF:%s ID:%s] useDevice=%v skor=%d/%d alasan=%v presence=%v\n",
		groupID, su, string(ctx.Msg.Info.ID), dev, det.DeviceID, det.Verdict, det.BaileysScore,
		det.TKCheck, det.DVCheck, det.PFCheck, det.IDCheck, det.IsUseDevice,
		score, antibotThreshold, reasons, presenceActive())
	fmt.Printf("[ANTIBOT-STRUCT] id=%q konten=%s wrap=%v MCI=%v ctx=%v unk[top:%t mci:%t ctx:%t] notes=%v\n",
		string(ctx.Msg.Info.ID), sf.ContentType, sf.Wrappers, sf.MCIFields, sf.CtxFields,
		sf.UnknownTop != "", sf.UnknownMCI != "", sf.UnknownCtx != "", sf.Notes)

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
	_, _ = ctx.Client.SendMessage(context.Background(), ctx.ChatJID, revoke, src.AndroidExtra())

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

// reBotCommandPrefix: pesan yang DIMULAI prefix command umum bot (. ! # /) diikuti
// huruf/digit — mis. ".menu", "#owner", "/start", "!ping". Punctuation murni
// ("...", "!!!") TIDAK cocok.
var reBotCommandPrefix = regexp.MustCompile(`^[.!#/][a-zA-Z0-9]`)

// isBotCommandPrefix true bila teks tampak seperti pemanggilan command bot.
func isBotCommandPrefix(text string) bool {
	return reBotCommandPrefix.MatchString(strings.TrimSpace(text))
}

// isPrefixSpamming mencatat satu pesan command-prefix dari pengirim & true bila
// jumlahnya >=prefixMax dalam prefixWindow. Hanya panggil untuk pesan yang memang
// command-prefix (lihat pemanggil yang men-gate dengan isBotCommandPrefix).
func isPrefixSpamming(key string) bool {
	now := time.Now()
	prefixMu.Lock()
	defer prefixMu.Unlock()

	var recent []time.Time
	for _, t := range prefixStore[key] {
		if now.Sub(t) < prefixWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	prefixStore[key] = recent
	return len(recent) >= prefixMax
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

// loadGroupAdmins mengambil (dari cache, atau refresh GetGroupInfo) daftar admin
// grup + apakah BOT sendiri admin. Satu pengambilan dipakai bersama oleh
// isCachedAdmin (cek pengirim) dan botIsGroupAdmin (cek bot).
func loadGroupAdmins(ctx *ContextBot, groupID string) adminCacheEntry {
	adminCacheMu.Lock()
	e, ok := adminCache[groupID]
	adminCacheMu.Unlock()
	if ok && time.Now().Before(e.exp) {
		return e
	}

	e = adminCacheEntry{users: make(map[string]bool), exp: time.Now().Add(adminCacheTL)}

	// JID bot sendiri (phone + lid) untuk menentukan status admin bot.
	var botUser, botLID string
	if ctx.Client.Store != nil {
		if ctx.Client.Store.ID != nil {
			botUser = ctx.Client.Store.ID.ToNonAD().User
		}
		if lid := ctx.Client.Store.GetLID(); !lid.IsEmpty() {
			botLID = lid.ToNonAD().User
		}
	}

	if info, err := ctx.Client.GetGroupInfo(context.Background(), ctx.ChatJID); err == nil {
		for _, p := range info.Participants {
			if !(p.IsAdmin || p.IsSuperAdmin) {
				continue
			}
			pu := p.JID.ToNonAD().User
			pl := p.LID.ToNonAD().User
			if pu != "" {
				e.users[pu] = true
			}
			if pl != "" {
				e.users[pl] = true
			}
			if (botUser != "" && (pu == botUser || pl == botUser)) ||
				(botLID != "" && (pu == botLID || pl == botLID)) {
				e.botAdmin = true
			}
		}
	}

	adminCacheMu.Lock()
	adminCache[groupID] = e
	adminCacheMu.Unlock()
	return e
}

// groupModExempt: user kebal moderasi (antibot/antilink) bila trusted ATAU admin
// grup. Satu sumber kebenaran pengecualian — dipakai bersama oleh kedua handler.
func groupModExempt(ctx *ContextBot, groupID string) bool {
	su := ctx.SenderJID.ToNonAD().User
	sl := ctx.SenderAlt.ToNonAD().User
	if (su != "" && src.DB.IsTrusted(groupID, su)) || (sl != "" && src.DB.IsTrusted(groupID, sl)) {
		return true
	}
	return isCachedAdmin(ctx, groupID)
}

func isCachedAdmin(ctx *ContextBot, groupID string) bool {
	e := loadGroupAdmins(ctx, groupID)
	su := ctx.SenderJID.ToNonAD().User
	sl := ctx.SenderAlt.ToNonAD().User
	return (su != "" && e.users[su]) || (sl != "" && e.users[sl])
}

// botIsGroupAdmin: apakah bot sendiri admin di grup ini. Antibot hanya berguna
// bila bot admin (kalau tidak, captcha/kick mustahil dijalankan).
func botIsGroupAdmin(ctx *ContextBot, groupID string) bool {
	return loadGroupAdmins(ctx, groupID).botAdmin
}

// ====================== COMMAND: TOGGLE ======================

func ExecuteAntibotToggle(ctx *ContextBot) error {
	if admin, _ := isUserAdmin(ctx); !admin {
		return ctx.Reply("⛔ Hanya admin yang bisa mengatur anti-bot.")
	}
	groupID := ctx.ChatJID.ToNonAD().String()
	sub := strings.ToLower(strings.TrimSpace(ctx.Args))

	switch sub {
	case "on":
		src.DB.SetGroupAntibot(groupID, true)
		return ctx.Reply("🛡️ *Anti-Bot AKTIF*.")
	case "off":
		src.DB.SetGroupAntibot(groupID, false)
		return ctx.Reply("🛡️ *Anti-Bot NONAKTIF*.")
	case "status", "show":
		return antibotStatus(ctx, groupID)
	default:
		return ctx.Reply(antibotHelp())
	}
}

func antibotStatus(ctx *ContextBot, groupID string) error {
	status := "❌ OFF"
	if src.DB.IsGroupAntibot(groupID) {
		status = "✅ ON"
	}
	botAdmin := "❌ bukan admin (antibot tak jalan)"
	if botIsGroupAdmin(ctx, groupID) {
		botAdmin = "✅ admin"
	}
	trusted := len(src.DB.GetTrustList(groupID))
	return ctx.Reply(fmt.Sprintf(
		"🛡️ *Anti-Bot*\n\nStatus  : %s\nBot     : %s\nTrusted : %d user\n\n_Bantuan: `antibot help`._",
		status, botAdmin, trusted))
}

func antibotHelp() string {
	return "📖 *Anti-Bot*\n\n" +
		"`antibot on` / `antibot off`\n" +
		"`antibot status` — status & info grup\n\n"
}
