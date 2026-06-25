package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// PLAYCALL — telepon seseorang lalu putarkan lagu.
//
//   playcall <judul>                  → telepon PENGIRIM perintah
//   playcall <nomor> <judul>          → telepon nomor tersebut
//   (reply pesan) playcall <judul>    → telepon pengirim pesan yang di-reply
//   playcall <judul> --all            → tampilkan daftar, user pilih lagunya
//
// Semua panggilan diserialkan lewat ANTRIAN (satu panggilan aktif pada satu
// waktu). Bila error saat menyiapkan lagu / menelepon, sistem RETRY otomatis
// di BACKGROUND (tanpa memberi tahu user) sebelum menyerah.
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Play Call",
		Category:    "General",
		Aliases:     []string{"playcall"},
		Pattern:     regexp.MustCompile(`(?i)^playcall(?:\s+(.+))?$`),
		Description: "Telepon seseorang & putarkan lagu (playcall <nomor?> <judul>, reply, atau --all)",
		Execute:     ExecutePlayCall,
	})
}

// ── ANTRIAN PANGGILAN (serial, satu call aktif pada satu waktu) ──

type playCallJob struct {
	client     *whatsmeow.Client
	chatJID    types.JID
	target     string // nomor / JID yang ditelepon (untuk StartCall)
	mentionJID string // JID lengkap untuk tag mention
	mentionNum string // bagian user (nomor) untuk teks "@nomor"
	song       ytSong
}

var (
	pcQueue     = make(chan playCallJob, 100)
	pcQueueOnce sync.Once
	pcPending   int32 // jumlah job antri + aktif (atomic)
)

func ensurePCWorker() {
	pcQueueOnce.Do(func() { go pcWorker() })
}

func pcWorker() {
	for job := range pcQueue {
		processPlayCall(job)
		atomic.AddInt32(&pcPending, -1)
	}
}

// enqueuePlayCall menambahkan job ke antrian, mengembalikan posisi (1 = langsung diproses).
func enqueuePlayCall(job playCallJob) int {
	ensurePCWorker()
	pos := int(atomic.AddInt32(&pcPending, 1))
	pcQueue <- job
	return pos
}

// startPlayCall memvalidasi & memasukkan permintaan panggilan ke antrian.
func startPlayCall(ctx *ContextBot, song ytSong, target, label string) error {
	if src.CallClient == nil {
		return ctx.Reply("⚠️ Subsistem panggilan belum aktif, tidak bisa menelepon.")
	}
	if target == "" {
		return ctx.Reply("⚠️ Target panggilan tidak valid.")
	}

	// Turunkan JID lengkap + nomor untuk tag mention.
	mentionJID := target
	if !strings.Contains(mentionJID, "@") {
		mentionJID = target + "@s.whatsapp.net"
	}
	mentionNum := mentionJID
	if jid, err := types.ParseJID(mentionJID); err == nil {
		mentionNum = jid.User
	}

	pos := enqueuePlayCall(playCallJob{
		client:     ctx.Client,
		chatJID:    ctx.ChatJID,
		target:     target,
		mentionJID: mentionJID,
		mentionNum: mentionNum,
		song:       song,
	})

	_ = ctx.React("📞")
	if pos > 1 {
		return ctx.Reply(fmt.Sprintf("⏳ *Sedang ada panggilan berlangsung.*\nPermintaanmu masuk *antrian posisi %d*.\n\n🎶 %s — %s\n📲 Tujuan: %s",
			pos, song.Name, song.Artist.Name, label))
	}
	return ctx.Reply(fmt.Sprintf("📞 *Menyiapkan panggilan...*\n🎶 %s — %s\n📲 Tujuan: %s",
		song.Name, song.Artist.Name, label))
}

// processPlayCall menjalankan satu job: unduh lagu (retry diam-diam) → telepon
// (retry diam-diam) → tunggu sampai panggilan berakhir. Memblokir agar serial.
func processPlayCall(job playCallJob) {
	send := func(text string) {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = job.client.SendMessage(c, job.chatJID, &waProto.Message{
			Conversation: proto.String(text),
		}, src.AndroidExtra())
	}
	sendMention := func(text string) {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = job.client.SendMessage(c, job.chatJID, &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text:        proto.String(text),
				ContextInfo: &waProto.ContextInfo{MentionedJID: []string{job.mentionJID}},
			},
		}, src.AndroidExtra())
	}

	// 1. Unduh audio dengan retry diam-diam.
	data, title, err := fetchSongAudioRetry(job.song, 3)
	if err != nil {
		send(fmt.Sprintf("❌ Gagal menyiapkan lagu: %v", err))
		return
	}

	tmp, err := os.CreateTemp("", "playcall-*.mp3")
	if err != nil {
		send("❌ Gagal menyiapkan file audio sementara.")
		return
	}
	tmpPath := tmp.Name()
	_, _ = tmp.Write(data)
	tmp.Close()
	defer os.Remove(tmpPath)

	// 2. Telepon dengan retry diam-diam (di background, tanpa memberi tahu user).
	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }

	notify := func(label string) {
		switch {
		case label == "ringing":
			send("📲 Berdering...")
		case label == "active":
			send("🟢 Tersambung! Memutar lagu...")
		case strings.HasPrefix(label, "ended"):
			reason := strings.TrimPrefix(label, "ended:")
			if reason == "" || reason == "ended" {
				send("🔴 Panggilan berakhir.")
			} else {
				send("🔴 Panggilan berakhir (" + reason + ").")
			}
			finish()
		}
	}

	var callID string
	var startErr error
	for attempt := 1; attempt <= 3; attempt++ {
		callID, _, startErr = src.StartCall(context.Background(), job.target, tmpPath, notify)
		if startErr == nil {
			break
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt*2) * time.Second) // backoff diam-diam
		}
	}
	if startErr != nil {
		send(fmt.Sprintf("❌ Gagal menelepon: %v", startErr))
		return
	}

	// Pesan utama dengan TAG MENTION ke tujuan.
	msg := fmt.Sprintf("☎️ *Memanggil & memutar lagu...*\n👤 @%s\n🎶 %s — %s",
		job.mentionNum, job.song.Name, job.song.Artist.Name)
	if job.song.Duration > 0 {
		msg += "\n⏱️ " + fmtDuration(job.song.Duration)
	}
	msg += "\n🆔 " + callID
	sendMention(msg)
	_ = title

	// 3. Tunggu sampai panggilan berakhir (pengaman 10 menit).
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		src.HangupCall(callID)
	}
}

// fetchSongAudioRetry mencoba mengunduh audio lagu beberapa kali (diam-diam).
func fetchSongAudioRetry(song ytSong, attempts int) ([]byte, string, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		data, title, err := fetchSongAudio(song)
		if err == nil && len(data) > 0 {
			return data, title, nil
		}
		lastErr = err
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("audio kosong")
	}
	return nil, "", lastErr
}

// fetchSongAudio mengambil tautan mp3 lalu mengunduh byte-nya.
func fetchSongAudio(song ytSong) ([]byte, string, error) {
	mp3Res, err := src.YtMp3(song.URL)
	if err != nil {
		return nil, "", err
	}
	var mp3 ytMp3Res
	if b, e := json.Marshal(mp3Res.Data); e == nil {
		json.Unmarshal(b, &mp3)
	}
	if !mp3.Success || mp3.Result.DownloadURL == "" {
		return nil, "", fmt.Errorf("tautan audio tidak tersedia")
	}
	data, _, err := src.DownloadBytes(mp3.Result.DownloadURL)
	if err != nil || len(data) == 0 {
		return nil, "", fmt.Errorf("gagal unduh audio: %v", err)
	}
	title := mp3.Result.Title
	if title == "" {
		title = song.Name
	}
	return data, title, nil
}

// ── DAFTAR LAGU (--all) ──

type pcAllSession struct {
	Songs  []ytSong
	Target string
	Label  string
}

// sendPlayCallList menampilkan daftar lagu (rich card) lalu mendaftarkannya ke
// reply-router agar user bisa membalas NOMOR untuk memilih lagu yang ditelepon.
func sendPlayCallList(ctx *ContextBot, query string, songs []ytSong, target, label string) error {
	rb := src.NewAIRich().
		SetTitle(fmt.Sprintf("☎️ Playcall: %s", query)).
		SetFooter("Balas dengan NOMOR untuk memutar lagu")

	for i, s := range songs {
		rb.AddText(fmt.Sprintf("*%d.* %s — %s", i+1, s.Name, s.Artist.Name))
		rb.AddProduct(src.AIProduct{
			Title:      s.Name,
			Brand:      s.Artist.Name,
			Price:      fmtDuration(s.Duration),
			SalePrice:  s.Album.Name,
			ProductURL: s.URL,
			ImageURL:   s.thumb(),
		})
	}

	msgID, err := rb.SendToChatWithID(ctx)
	if err != nil {
		return ctx.Reply("❌ Gagal menampilkan daftar lagu.")
	}
	replyRouter.Register(msgID, "playcall", &pcAllSession{Songs: songs, Target: target, Label: label})
	return nil
}

// handlePlayCallReply menangani reply nomor dari daftar playcall --all.
// Mengembalikan true bila reply benar-benar memilih lagu (dikonsumsi).
func handlePlayCallReply(ctx *ContextBot, rc *ReplyContext) bool {
	sess, ok := rc.Data.(*pcAllSession)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.TextMessage))
	if err != nil || n < 1 || n > len(sess.Songs) {
		return false
	}
	_ = startPlayCall(ctx, sess.Songs[n-1], sess.Target, sess.Label)
	return true
}

// ── COMMAND HANDLER ──

var phoneRe = regexp.MustCompile(`(\+?\d[\d\s\-]{6,}\d)`)

func ExecutePlayCall(ctx *ContextBot) error {
	raw := strings.TrimSpace(ctx.Args)
	if raw == "" {
		return ctx.Reply("☎️ *PLAYCALL*\n\nTelepon seseorang & putarkan lagu.\n\n• `playcall <judul>` — telepon kamu sendiri\n• `playcall <nomor> <judul>` — telepon nomor itu\n• reply pesan + `playcall <judul>` — telepon pengirim pesan itu\n• `playcall <judul> --all` — pilih lagu dari daftar")
	}

	// Deteksi flag --all lebih dulu agar tidak mengganggu deteksi nomor.
	showAll := false
	if regexp.MustCompile(`(?i)(^|\s)--all(\s|$)`).MatchString(raw) {
		showAll = true
		raw = strings.TrimSpace(regexp.MustCompile(`(?i)\s*--all\s*`).ReplaceAllString(raw, " "))
	}

	// 1. Tentukan target.
	target := ""
	label := ""

	// a) Nomor telepon di dalam query (prioritas tertinggi).
	if m := phoneRe.FindString(raw); m != "" {
		digits := regexp.MustCompile(`\D`).ReplaceAllString(m, "")
		if len(digits) >= 8 && len(digits) <= 15 {
			target = digits
			label = "@" + digits
			raw = strings.TrimSpace(strings.Replace(raw, m, "", 1))
		}
	}

	// b) Reply ke sebuah pesan → telepon pengirim pesan itu.
	if target == "" {
		if ci := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo(); ci != nil {
			if p := ci.GetParticipant(); p != "" {
				target = p
				if jid, err := types.ParseJID(p); err == nil {
					label = "@" + jid.User
				} else {
					label = p
				}
			}
		}
	}

	// c) Default → telepon pengirim perintah.
	if target == "" {
		target = ctx.SenderJID.String()
		label = "@" + ctx.SenderJID.User
	}

	query := strings.TrimSpace(raw)
	if query == "" {
		return ctx.Reply("🎵 Sebutkan judul lagunya.\nContoh: `playcall despacito`")
	}

	_ = ctx.React("🔎")
	songs, err := searchSongs(query)
	if err != nil || len(songs) == 0 {
		return ctx.Reply(fmt.Sprintf("😢 Lagu *%s* tidak ditemukan.", query))
	}

	if showAll {
		return sendPlayCallList(ctx, query, songs, target, label)
	}
	return startPlayCall(ctx, pickRandomSong(songs), target, label)
}
