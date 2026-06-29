package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// PLAY MUSIC dari YouTube Music (API ps.azumi.dev)
//   play <judul>         → langsung kirim hasil terbaik
//   play <judul> --all   → tampilkan daftar lagu, user reply nomor
//   play <judul> --call  → telepon pengirim & putar lagu acak saat tersambung
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Play Music",
		Category:    "General",
		Aliases:     []string{"play", "ytmusic"},
		Pattern:     regexp.MustCompile(`(?i)^(?:play|ytmusic)\s+(.+)`),
		Description: "Putar lagu YouTube Music (--all pilih dari daftar, --call telepon pengirim)",
		Execute:     ExecutePlay,
	})
}

type ytSong struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Name   string `json:"name"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		Name string `json:"name"`
	} `json:"album"`
	Duration   int `json:"duration"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

type ytSearchRes struct {
	Success bool     `json:"success"`
	Result  []ytSong `json:"result"`
}

type ytMp3Res struct {
	Success bool `json:"success"`
	Result  struct {
		VideoInfo struct {
			Title    string `json:"title"`
			Duration int    `json:"duration"`
		} `json:"videoInfo"`
		// API ps.azumi.dev terbaru: tautan unduh ada di "downloadUrl"
		// (sebelumnya "downloadURL" + "title" flat di result).
		DownloadURL string `json:"downloadUrl"`
	} `json:"result"`
}

// mp3Title mengambil judul dari respons ytmp3 (struktur baru: result.videoInfo.title).
func (r ytMp3Res) mp3Title() string { return r.Result.VideoInfo.Title }

// ytMusicSession menyimpan hasil pencarian untuk dipilih lewat reply.
type ytMusicSession struct {
	Songs []ytSong
}

func (s ytSong) thumb() string {
	if len(s.Thumbnails) > 0 {
		return s.Thumbnails[len(s.Thumbnails)-1].URL // ambil resolusi terbesar
	}
	return ""
}

func fmtDuration(sec int) string {
	if sec <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func ExecutePlay(ctx *ContextBot) error {
	raw := strings.TrimSpace(ctx.Args)
	if raw == "" {
		return ctx.Reply("🎵 Mau putar lagu apa?\nContoh: `play trouble is a friend`\nPilih dari daftar: `play trouble --all`")
	}

	// Deteksi flag --all
	showAll := false
	if regexp.MustCompile(`(?i)(^|\s)--all(\s|$)`).MatchString(raw) {
		showAll = true
		raw = strings.TrimSpace(regexp.MustCompile(`(?i)\s*--all\s*`).ReplaceAllString(raw, " "))
	}

	// Deteksi flag --call
	wantCall := false
	if regexp.MustCompile(`(?i)(^|\s)--call(\s|$)`).MatchString(raw) {
		wantCall = true
		raw = strings.TrimSpace(regexp.MustCompile(`(?i)\s*--call\s*`).ReplaceAllString(raw, " "))
	}

	query := strings.TrimSpace(raw)
	if query == "" {
		return ctx.Reply("⚠️ Sebutkan judul lagunya.\n\n📌 *Cara pakai:* `play trouble --all`")
	}

	go func() { _ = ctx.React("⏳") }()
	songs, err := searchSongs(query)
	if err != nil || len(songs) == 0 {
		_ = ctx.React("❌")
		return ctx.Reply(fmt.Sprintf("❌ Lagu *%s* tidak ditemukan.", query))
	}

	if wantCall {
		// Mode --call: telepon pengirim & putar lagu ACAK (bukan hasil teratas).
		return playCall(ctx, pickRandomSong(songs))
	}

	if showAll {
		return sendSongList(ctx, query, songs)
	}

	// Mode langsung: pilih hasil pertama bertipe SONG
	best := songs[0]
	for _, s := range songs {
		if strings.EqualFold(s.Type, "SONG") {
			best = s
			break
		}
	}
	return playSong(ctx, best, false) // tanpa --all → metadata teks biasa
}

// pickRandomSong memilih satu lagu acak dari hasil pencarian, mengutamakan
// entri bertipe SONG (mengabaikan VIDEO/PODCAST dsb). Bila tak ada yang
// bertipe SONG, ambil acak dari seluruh hasil.
func pickRandomSong(songs []ytSong) ytSong {
	var onlySongs []ytSong
	for _, s := range songs {
		if strings.EqualFold(s.Type, "SONG") {
			onlySongs = append(onlySongs, s)
		}
	}
	if len(onlySongs) > 0 {
		return onlySongs[rand.IntN(len(onlySongs))]
	}
	return songs[rand.IntN(len(songs))]
}

// searchSongs memanggil API dan mengembalikan daftar lagu (maks 10).
func searchSongs(query string) ([]ytSong, error) {
	res, err := src.YtMusicSearch(query)
	if err != nil {
		return nil, err
	}
	var parsed ytSearchRes
	if b, e := json.Marshal(res.Data); e == nil {
		json.Unmarshal(b, &parsed)
	}
	if !parsed.Success {
		return nil, fmt.Errorf("pencarian gagal")
	}
	songs := parsed.Result
	if len(songs) > 10 {
		songs = songs[:10]
	}
	return songs, nil
}

// sendSongList menampilkan daftar lagu (rich card + thumbnail) dan mendaftarkan
// ke reply-router agar user bisa reply nomor untuk memilih.
func sendSongList(ctx *ContextBot, query string, songs []ytSong) error {
	rb := src.NewAIRich().SetTitle(fmt.Sprintf("🎵 Hasil: %s", query)).
		SetFooter("Balas dengan NOMOR lagu untuk memutar")

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
	replyRouter.Register(msgID, "ytmusic", &ytMusicSession{Songs: songs})
	return nil
}

// handleYtMusicReply menangani reply nomor dari daftar lagu.
// Mengembalikan true bila reply benar-benar memilih lagu (dikonsumsi).
// Bila reply TIDAK berkaitan dengan daftar (bukan nomor valid), return false
// agar pesan tidak direspon play-music & bisa diproses normal.
func handleYtMusicReply(ctx *ContextBot, rc *ReplyContext) bool {
	sess, ok := rc.Data.(*ytMusicSession)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.TextMessage))
	if err != nil || n < 1 || n > len(sess.Songs) {
		return false // bukan pilihan valid → jangan respon di play music
	}
	_ = playSong(ctx, sess.Songs[n-1], true) // dari daftar --all → kartu AIRich
	return true
}

// playSong mengambil audio lalu mengirim metadata + audio playable.
// useCard=true → kartu AIRich (preview link); false → metadata teks biasa.
func playSong(ctx *ContextBot, song ytSong, useCard bool) error {
	go func() { _ = ctx.React("⏳") }()
	mp3Res, err := src.YtMp3(song.URL)
	if err != nil {
		return ctx.Reply("❌ Gagal mengambil audio lagu ini.")
	}
	var mp3 ytMp3Res
	if b, e := json.Marshal(mp3Res.Data); e == nil {
		json.Unmarshal(b, &mp3)
	}
	if !mp3.Success || mp3.Result.DownloadURL == "" {
		return ctx.Reply("❌ Tautan audio tidak tersedia untuk lagu ini.")
	}

	data, _, err := src.DownloadBytes(mp3.Result.DownloadURL)
	if err != nil || len(data) == 0 {
		return ctx.Reply("❌ Gagal mengunduh audio.")
	}

	title := mp3.mp3Title()
	if title == "" {
		title = song.Name
	}

	if useCard {
		// Kartu metadata + preview link (gaya anichin)
		_ = src.NewAIRich().
			SetTitle("🎶 Now Playing").
			AddProduct(src.AIProduct{
				Title:      title,
				Brand:      song.Artist.Name,
				Price:      fmtDuration(song.Duration),
				SalePrice:  song.Album.Name,
				ProductURL: song.URL,
				ImageURL:   song.thumb(),
			}).
			SendToChat(ctx)
	} else {
		// Metadata teks biasa
		info := fmt.Sprintf("🎶 *%s*\n👤 %s", title, song.Artist.Name)
		if song.Album.Name != "" {
			info += fmt.Sprintf("\n💽 %s", song.Album.Name)
		}
		if song.Duration > 0 {
			info += fmt.Sprintf("\n⏱️ %s", fmtDuration(song.Duration))
		}
		if song.URL != "" {
			info += fmt.Sprintf("\n🔗 %s", song.URL)
		}
		_ = ctx.Reply(info)
	}

	if err := sendAudio(ctx, data, song.Duration); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Gagal mengirim audio: %v", err))
	}
	_ = ctx.React("✅")
	return nil
}

// playCall menelepon PENGIRIM perintah dan memutarkan lagu, lewat antrian
// panggilan bersama (serial + retry). Lihat playcall.go.
func playCall(ctx *ContextBot, song ytSong) error {
	target := ctx.SenderJID.String()
	if target == "" {
		return ctx.Reply("⚠️ Tidak bisa menentukan nomor pengirim untuk ditelepon.")
	}
	return startPlayCall(ctx, song, target, "+"+ctx.SenderJID.User)
}

// sendAudio mengunggah & mengirim audio playable (bukan voice note).
func sendAudio(ctx *ContextBot, data []byte, seconds int) error {
	uploaded, err := ctx.Client.Upload(context.Background(), data, whatsmeow.MediaAudio)
	if err != nil {
		return err
	}

	msg := &waProto.Message{
		AudioMessage: &waProto.AudioMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("audio/mpeg"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			PTT:           proto.Bool(false),
		},
	}
	if seconds > 0 {
		msg.AudioMessage.Seconds = proto.Uint32(uint32(seconds))
	}

	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID.ToNonAD(), msg, src.AndroidExtra())
	return err
}
