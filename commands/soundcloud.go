package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// =============================================
// 1. STATE DATA (di-key oleh ID pesan via reply_router)
// =============================================

type SoundcloudSessionData struct {
	TrackURLs []string
	ExpiresAt time.Time // tidak dipakai lagi (expiry ditangani reply_router)
}

// =============================================
// 2. JSON STRUCTS
// =============================================

type SoundcloudSearchRes struct {
	Success bool `json:"success"`
	Result  struct {
		Collection []struct {
			Title        string `json:"title"`
			PermalinkURL string `json:"permalink_url"`
			Genre        string `json:"genre"`
			User         struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"collection"`
	} `json:"result"`
}

type SoundcloudFetchRes struct {
	Success bool `json:"success"`
	Result  struct {
		Title     string   `json:"title"`
		Uploader  string   `json:"uploader"`
		Thumbnail string   `json:"thumbnail"`
		URL       string   `json:"url"` // Ini stream utama (.m3u8)
		Tags      []string `json:"tags"`
		Formats   []struct {
			FormatID string `json:"format_id"`
			URL      string `json:"url"`
		} `json:"formats"`
	} `json:"result"`
}

// =============================================
// 3. COMMAND REGISTRATION
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "SoundCloud Downloader",
		Category:    "Downloader",
		Aliases:     []string{"sc", "soundcloud"},
		Pattern:     regexp.MustCompile(`(?i)^(?:sc|soundcloud|scdl)\s+(.+)$`),
		Description: "Mencari atau mendownload lagu dari SoundCloud",
		//Price:       0.010,
		Execute: ExecuteSoundcloud,
	})
}

// =============================================
// 4. PHASE 1: SEARCH ATAU DIRECT FETCH
// =============================================

func ExecuteSoundcloud(ctx *ContextBot) error {
	query := strings.TrimSpace(ctx.Args)

	if query == "" {
		return ctx.Reply("❌ Format salah. Contoh: *Play about you*")
	}

	_ = ctx.Reply("⏳ Memproses pencarian lagu...")

	// Cek apakah input adalah direct URL
	urlRegex := regexp.MustCompile(`(?i)(https?:\/\/(?:www\.|m\.)?soundcloud\.com\/[^\s]+)`)
	if urlRegex.MatchString(query) {
		// Bypass pencarian, langsung proses URL
		targetUrl := urlRegex.FindString(query)
		return loadSoundcloudTrack(ctx, targetUrl)
	}

	// Jika bukan URL, lakukan pencarian ke API
	apiUrl := fmt.Sprintf("https://www.ashema.my.id/d/finder/soundcloud?q=%s", url.QueryEscape(query))
	resp, err := http.Get(apiUrl)
	if err != nil {
		return ctx.Reply("❌ Gagal terhubung ke API SoundCloud.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data SoundcloudSearchRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success || len(data.Result.Collection) == 0 {
		return ctx.Reply(fmt.Sprintf("❌ Tidak ditemukan hasil untuk: *%s*", query))
	}

	newSession := &SoundcloudSessionData{}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 *Hasil Pencarian SoundCloud: %s*\n\n", strings.ToUpper(query)))

	for i, track := range data.Result.Collection {
		if i >= 10 { // Batasi 10 hasil pencarian
			break
		}

		genre := track.Genre
		if genre == "" {
			genre = "Unknown Genre"
		}

		sb.WriteString(fmt.Sprintf("*%d.* %s\n", i+1, track.Title))
		sb.WriteString(fmt.Sprintf("└ 👤 %s | 🎵 %s\n\n", track.User.Username, genre))

		newSession.TrackURLs = append(newSession.TrackURLs, track.PermalinkURL)
	}

	sb.WriteString("---\nReply pesan ini dengan angka (1-10) untuk memutar lagu.")

	// Kirim list & daftarkan ID pesan ke reply-router
	msgID, err := ctx.ReplyWithID(sb.String())
	if err == nil {
		replyRouter.Register(msgID, "soundcloud", newSession)
	}
	return err
}

// =============================================
// 5. PHASE 2: INTERACTION HANDLER (via reply_router)
// =============================================

func handleSoundcloudReply(ctx *ContextBot, rc *ReplyContext) error {
	session, ok := rc.Data.(*SoundcloudSessionData)
	if !ok {
		return nil
	}

	textRaw := strings.ToLower(strings.TrimSpace(ctx.TextMessage))
	inputNum, err := strconv.Atoi(textRaw)
	if err != nil {
		return nil // Abaikan jika bukan angka
	}

	if inputNum < 1 || inputNum > len(session.TrackURLs) {
		return nil // Out of bounds
	}

	selectedURL := session.TrackURLs[inputNum-1]
	return loadSoundcloudTrack(ctx, selectedURL)
}

// =============================================
// 6. PHASE 3: FETCH & SEND BUTTON
// =============================================

func loadSoundcloudTrack(ctx *ContextBot, trackURL string) error {
	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://www.ashema.my.id/d/fetcher/soundcloud?url=%s", url.QueryEscape(trackURL))
	resp, err := http.Get(apiUrl)
	if err != nil {
		return ctx.Reply("❌ Gagal memuat data lagu dari server.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data SoundcloudFetchRes
	ctx.Print(data)
	if err := json.Unmarshal(body, &data); err != nil || !data.Success || data.Result.URL == "" {
		return ctx.Reply("❌ Gagal memproses audio lagu ini.")
	}

	res := data.Result

	// MENCARI LINK PROGRESSIVE MP3 (http_mp3_1_0) AGAR LEBIH STABIL SAAT DI-STREAM
	audioLink := res.URL
	for _, f := range res.Formats {
		if f.FormatID == "http_mp3_1_0" && f.URL != "" {
			audioLink = f.URL
			break
		}
	}

	if audioLink == "" {
		return ctx.Reply("❌ Gagal mendapatkan tautan audio streaming.")
	}

	// Merakit Link Streaming Audio
	finalAudioURL := fmt.Sprintf("https://ashema.my.id/watch?url=%s&type=audio", url.QueryEscape(audioLink))

	// Inisialisasi komponen Button
	btn := ctx.Button()
	btn.SetTitle("☁️ *SOUNDCLOUD PLAYER*")

	genre := "Alternative"
	if len(res.Tags) > 0 {
		genre = res.Tags[0]
	}

	var bodyMsg strings.Builder
	bodyMsg.WriteString(fmt.Sprintf("🎧 *%s*\n\n", res.Title))
	bodyMsg.WriteString(fmt.Sprintf("👤 *Uploader:* %s\n", res.Uploader))
	bodyMsg.WriteString(fmt.Sprintf("🎵 *Genre:* %s\n", genre))
	bodyMsg.WriteString("\nPilih tombol di bawah untuk mulai memutar audio.")

	btn.SetBody(bodyMsg.String())
	btn.SetFooter("© Powered By SoundCloud")

	btn.AddUrl("▶️ Putar Audio", finalAudioURL, true)

	return btn.SendToChat(ctx)
}
