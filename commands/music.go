package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// PLAY MUSIC dari YouTube Music (API ps.azumi.dev)
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Play Music",
		Category:    "General",
		Aliases:     []string{"play", "ytmusic"},
		Pattern:     regexp.MustCompile(`(?i)^(?:play|ytmusic)\s+(.+)`),
		Description: "Putar/kirim audio lagu dari YouTube Music",
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
	Duration int `json:"duration"`
}

type ytSearchRes struct {
	Success bool     `json:"success"`
	Result  []ytSong `json:"result"`
}

type ytMp3Res struct {
	Success bool `json:"success"`
	Result  struct {
		Title       string `json:"title"`
		Format      string `json:"format"`
		DownloadURL string `json:"downloadURL"`
	} `json:"result"`
}

func ExecutePlay(ctx *ContextBot) error {
	query := strings.TrimSpace(ctx.Args)
	if query == "" {
		return ctx.Reply("🎵 Mau putar lagu apa?\nContoh: `play trouble is a friend`")
	}

	_ = ctx.React("⏳")

	// 1. Cari lagu di YouTube Music
	searchRes, err := src.YtMusicSearch(query)
	if err != nil {
		return ctx.Reply("❌ Gagal mencari lagu. Coba lagi nanti.")
	}
	var search ytSearchRes
	if b, e := json.Marshal(searchRes.Data); e == nil {
		json.Unmarshal(b, &search)
	}
	if !search.Success || len(search.Result) == 0 {
		return ctx.Reply(fmt.Sprintf("😢 Lagu *%s* tidak ditemukan.", query))
	}

	// Pilih hasil pertama bertipe SONG (atau hasil pertama apa pun)
	song := search.Result[0]
	for _, s := range search.Result {
		if strings.EqualFold(s.Type, "SONG") {
			song = s
			break
		}
	}

	// 2. Ambil tautan unduhan mp3
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

	// 3. Unduh bytes mp3
	data, _, err := src.DownloadBytes(mp3.Result.DownloadURL)
	if err != nil || len(data) == 0 {
		return ctx.Reply("❌ Gagal mengunduh audio.")
	}

	// 4. Kirim info + audio playable
	title := mp3.Result.Title
	if title == "" {
		title = song.Name
	}
	info := fmt.Sprintf("🎶 *%s*\n👤 %s", title, song.Artist.Name)
	if song.Album.Name != "" {
		info += fmt.Sprintf("\n💽 %s", song.Album.Name)
	}
	if song.Duration > 0 {
		info += fmt.Sprintf("\n⏱️ %d:%02d", song.Duration/60, song.Duration%60)
	}
	_ = ctx.Reply(info)

	if err := sendAudio(ctx, data, song.Duration); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Gagal mengirim audio: %v", err))
	}
	_ = ctx.React("✅")
	return nil
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

	_, err = ctx.Client.SendMessage(context.Background(), ctx.ChatJID.ToNonAD(), msg)
	return err
}
