package src

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// SISTEM PREMIUM
// =================================================================
// - Owner SELALU premium.
// - User premium bisa MENJALANKAN command ber-`Premium:true` & MELIHAT hasilnya.
// - Di grup, hasil command premium DISEMBUNYIKAN dari member non-premium lewat
//   sistem exclude (src/exclude.go) → mereka dapat placeholder "Menunggu pesan ini".
// - Premium punya BATAS WAKTU (expires_at unix; 0 = permanen).
//
// Kunci penyimpanan = NOMOR (bagian user JID, tanpa device). Pengecekan mencocokkan
// SEMUA bentuk nomor sebuah identitas (LID & PN) agar tak meleset antar-addressing.
// =================================================================

// PremiumEntry = satu baris data premium.
type PremiumEntry struct {
	Number    string
	ExpiresAt int64 // 0 = permanen
}

// AddPremium menamb/memperpanjang premium untuk `number` sampai `expiresAt`
// (unix detik; 0 = permanen).
func (db *Database) AddPremium(number string, expiresAt int64) {
	if db == nil || number == "" {
		return
	}
	db.db.Exec(`INSERT INTO premium_users (number, expires_at, added_at) VALUES (?, ?, ?)
		ON CONFLICT(number) DO UPDATE SET expires_at=excluded.expires_at`,
		number, expiresAt, time.Now().Unix())
}

// RemovePremium mencabut premium sebuah nomor. true bila ada yang terhapus.
func (db *Database) RemovePremium(number string) bool {
	if db == nil || number == "" {
		return false
	}
	res, err := db.db.Exec("DELETE FROM premium_users WHERE number=?", number)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// IsPremiumNumber true bila nomor punya premium yang BELUM kedaluwarsa. Baris yang
// sudah kedaluwarsa dibersihkan (lazy).
func (db *Database) IsPremiumNumber(number string) bool {
	if db == nil || number == "" {
		return false
	}
	var exp int64
	err := db.db.QueryRow("SELECT expires_at FROM premium_users WHERE number=?", number).Scan(&exp)
	if err != nil {
		return false
	}
	if exp == 0 {
		return true // permanen
	}
	if time.Now().Unix() > exp {
		db.db.Exec("DELETE FROM premium_users WHERE number=?", number)
		return false
	}
	return true
}

// ListPremium mengembalikan semua entri premium yang masih berlaku (membersihkan
// yang kedaluwarsa).
func (db *Database) ListPremium() []PremiumEntry {
	if db == nil {
		return nil
	}
	rows, err := db.db.Query("SELECT number, expires_at FROM premium_users ORDER BY added_at DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := time.Now().Unix()
	var out []PremiumEntry
	var expired []string
	for rows.Next() {
		var e PremiumEntry
		if rows.Scan(&e.Number, &e.ExpiresAt) != nil {
			continue
		}
		if e.ExpiresAt != 0 && now > e.ExpiresAt {
			expired = append(expired, e.Number)
			continue
		}
		out = append(out, e)
	}
	for _, n := range expired {
		db.db.Exec("DELETE FROM premium_users WHERE number=?", n)
	}
	return out
}

// =================================================================
// PARSE DURASI: "30d", "12h", "1w", "60m", "45s", "perm"/"permanen"/"selamanya".
// Mengembalikan expiresAt (unix; 0 = permanen), label ramah, dan ok.
// =================================================================

var reDuration = regexp.MustCompile(`^(\d+)\s*(s|m|h|d|w|mo|y|detik|menit|jam|hari|minggu|bulan|tahun)?$`)

// ParsePremiumDuration menerjemahkan token durasi. Bila kosong → default 30 hari.
func ParsePremiumDuration(s string) (expiresAt int64, label string, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "perm", "permanen", "permanent", "selamanya", "unli", "unlimited", "0":
		return 0, "permanen", true
	case "":
		return time.Now().Add(30 * 24 * time.Hour).Unix(), "30 hari", true
	}
	m := reDuration.FindStringSubmatch(s)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, "", false
	}
	unit := m[2]
	var d time.Duration
	var uLabel string
	switch unit {
	case "s", "detik":
		d, uLabel = time.Second, "detik"
	case "m", "menit":
		d, uLabel = time.Minute, "menit"
	case "h", "jam":
		d, uLabel = time.Hour, "jam"
	case "", "d", "hari":
		d, uLabel = 24*time.Hour, "hari"
	case "w", "minggu":
		d, uLabel = 7*24*time.Hour, "minggu"
	case "mo", "bulan":
		d, uLabel = 30*24*time.Hour, "bulan"
	case "y", "tahun":
		d, uLabel = 365*24*time.Hour, "tahun"
	default:
		return 0, "", false
	}
	return time.Now().Add(time.Duration(n) * d).Unix(), fmt.Sprintf("%d %s", n, uLabel), true
}

// FormatPremiumRemaining membuat teks sisa waktu dari expiresAt.
func FormatPremiumRemaining(expiresAt int64) string {
	if expiresAt == 0 {
		return "permanen"
	}
	rem := time.Until(time.Unix(expiresAt, 0))
	if rem <= 0 {
		return "kedaluwarsa"
	}
	days := int(rem.Hours()) / 24
	hours := int(rem.Hours()) % 24
	mins := int(rem.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dh %dj", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dj %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// =================================================================
// PENGIRIMAN HASIL PREMIUM (exclude member non-premium di grup)
// =================================================================

// numbersOfMember mengumpulkan semua bentuk nomor (JID/LID/PN) seorang participant.
func numbersOfMember(p types.GroupParticipant) []string {
	nums := []string{p.JID.ToNonAD().User}
	if !p.LID.IsEmpty() {
		nums = append(nums, p.LID.ToNonAD().User)
	}
	if !p.PhoneNumber.IsEmpty() {
		nums = append(nums, p.PhoneNumber.ToNonAD().User)
	}
	return nums
}

// isMemberPremium true bila participant = owner atau punya premium aktif.
func isMemberPremium(p types.GroupParticipant) bool {
	for _, n := range numbersOfMember(p) {
		if n == "" {
			continue
		}
		if n == AppConfig.OwnerNumber {
			return true
		}
		if DB.IsPremiumNumber(n) {
			return true
		}
	}
	return false
}

// nonPremiumGroupMembers = daftar JID member grup yang BUKAN premium (untuk exclude).
func nonPremiumGroupMembers(ctx context.Context, client *whatsmeow.Client, chat types.JID) []types.JID {
	gi, err := client.GetGroupInfo(ctx, chat)
	if err != nil {
		return nil
	}
	var out []types.JID
	for _, p := range gi.Participants {
		if isMemberPremium(p) {
			continue
		}
		out = append(out, p.JID)
	}
	return out
}

// SendPremiumMessage mengirim `msg` ke `chat`. Di grup, member non-premium
// di-exclude (tak bisa baca). Di japri, kirim biasa. SELALU mengembalikan message-ID
// pesan terkirim (bukan phash) agar bisa didaftarkan ke reply-router.
func SendPremiumMessage(client *whatsmeow.Client, chat types.JID, msg *waProto.Message) (string, error) {
	return SendPremiumRaw(client, chat, msg, "")
}

// SendPremiumText = pembungkus teks untuk SendPremiumMessage.
func SendPremiumText(client *whatsmeow.Client, chat types.JID, text string) (string, error) {
	return SendPremiumMessage(client, chat, &waProto.Message{Conversation: proto.String(text)})
}

// SendPremiumRaw = versi low-level SendPremiumMessage: memakai `msgID` yang sudah
// ditentukan pemanggil (agar bisa didaftarkan ke reply-router) dan mendukung
// `extraNodes` (mis. <biz> native_flow utk tombol) yang harus ikut di stanza.
// Di grup ber-non-premium → jalur exclude (skmsg-hide). Selain itu → kirim normal.
func SendPremiumRaw(client *whatsmeow.Client, chat types.JID, msg *waProto.Message, msgID string, extraNodes ...waBinary.Node) (string, error) {
	ctx := context.Background()
	if msgID == "" {
		msgID = GenerateAndroidMessageID()
	}
	normalSend := func() (string, error) {
		extra := whatsmeow.SendRequestExtra{ID: msgID}
		if len(extraNodes) > 0 {
			nodes := append([]waBinary.Node(nil), extraNodes...)
			extra.AdditionalNodes = &nodes
		}
		_, err := client.SendMessage(ctx, chat.ToNonAD(), msg, extra)
		return msgID, err
	}
	if chat.Server != types.GroupServer {
		return normalSend()
	}
	exclude := nonPremiumGroupMembers(ctx, client, chat)
	if len(exclude) == 0 {
		// Semua member premium (atau info grup gagal) → kirim normal.
		return normalSend()
	}
	// PENTING: SendGroupExcluding mengembalikan phash, BUKAN message-ID. Reply-router
	// di-key oleh message-ID (yang dikutip saat user membalas), jadi kembalikan `msgID`
	// yang kita kontrol (= id stanza terkirim), bukan phash-nya.
	if _, err := SendGroupExcluding(ctx, client, chat, msg, msgID, exclude, false, extraNodes...); err != nil {
		return "", err
	}
	return msgID, nil
}

// PremiumImage (metode ContextBot) mengunggah & mengirim gambar sebagai hasil
// premium — otomatis meng-exclude member non-premium bila di grup.
func (ctx *ContextBot) PremiumImage(data []byte, mimeType, caption string) error {
	up, err := ctx.Client.Upload(context.Background(), data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("gagal upload gambar: %w", err)
	}
	length := uint64(len(data))
	msg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			URL:           &up.URL,
			DirectPath:    &up.DirectPath,
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    &length,
			Mimetype:      &mimeType,
		},
	}
	if caption != "" {
		msg.ImageMessage.Caption = &caption
	}
	_, err = SendPremiumMessage(ctx.Client, ctx.ChatJID, msg)
	return err
}

// PremiumReply = balasan teks premium (exclude non-premium di grup).
func (ctx *ContextBot) PremiumReply(text string) error {
	_, err := SendPremiumText(ctx.Client, ctx.ChatJID, text)
	return err
}
