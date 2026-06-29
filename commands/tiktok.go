package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "TikTok Downloader",
		Category:    "Downloader",
		Aliases:     []string{"tt", "ttdl", "tiktok"},
		Pattern:     regexp.MustCompile(`(?i)^(?:tiktok|ttdl|tt)\s+(.+)`),
		Description: "Unduh video TikTok — kirim media langsung (tambah --view untuk player link)",
		Price:       0.015,
		Execute:     ExecuteTikTok,
	})
}

func ExecuteTikTok(ctx *ContextBot) error {
	// --view → mode player link/button (perilaku lama). Tanpa flag → kirim media langsung.
	wantView := regexp.MustCompile(`(?i)(^|\s)--view(\s|$)`).MatchString(ctx.TextMessage)

	re := regexp.MustCompile(`(?i)(https?:\/\/(?:www\.|vt\.|vm\.)?tiktok\.com\/[^\s]+)`)
	matches := re.FindStringSubmatch(ctx.TextMessage)
	if len(matches) == 0 {
		return ctx.Reply("⚠️ Tidak menemukan URL TikTok yang valid.\n\nContoh:\n• `tt https://vt.tiktok.com/xxxx` — kirim video langsung\n• `tt https://vt.tiktok.com/xxxx --view` — tampilkan player link")
	}

	targetUrl := matches[1]
	go func() { _ = ctx.React("⏳") }()
	data := url.Values{}
	data.Set("q", targetUrl)
	data.Set("cursor", "0")
	data.Set("page", "0")
	data.Set("lang", "en")

	req, _ := http.NewRequest("POST", "https://savetikpro.com/api/ajaxSearch", strings.NewReader(data.Encode()))
	req.Header.Add("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("referer", "https://savetikpro.com/")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal terhubung ke server.")
	}
	defer resp.Body.Close()

	bodyText, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(bodyText, &result)

	if result["status"] != "ok" || result["data"] == nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal menemukan data. Pastikan URL benar dan tidak privat.")
	}

	htmlData, _ := result["data"].(string)
	pageType, _ := result["pageType"].(string)

	stripHTML := func(str string) string {
		reHTML := regexp.MustCompile(`<[^>]*>?`)
		return strings.TrimSpace(reHTML.ReplaceAllString(str, ""))
	}

	if pageType == "detail" {
		titleRe := regexp.MustCompile(`<h3>([\s\S]*?)<\/h3>`)
		title := "Tanpa Judul"
		if m := titleRe.FindStringSubmatch(htmlData); len(m) > 1 {
			title = stripHTML(m[1])
		}

		mp4Re := regexp.MustCompile(`(?i)<a[^>]*href="([^"]+)"[^>]*>.*?Download MP4.*?<\/a>`)
		mp4Matches := mp4Re.FindAllStringSubmatch(htmlData, -1)
		mp3Re := regexp.MustCompile(`(?i)<a[^>]*href="([^"]+)"[^>]*>.*?Download MP3.*?<\/a>`)
		mp3Match := mp3Re.FindStringSubmatch(htmlData)

		if len(mp4Matches) == 0 {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Tidak dapat menemukan tautan video pada halaman ini.")
		}

		bestVideoUrl := mp4Matches[0][1]
		if len(mp4Matches) >= 3 {
			bestVideoUrl = mp4Matches[2][1] // biasanya index ke-3 = kualitas terbaik
		}

		// ── MODE DEFAULT: kirim video langsung ──
		if !wantView {
			caption := fmt.Sprintf("🎬 *TikTok*\n📝 %s", title)
			if err := sendTikTokVideo(ctx, bestVideoUrl, caption); err != nil {
				// Fallback ke mode player bila download/kirim gagal.
				return tiktokSendPlayer(ctx, title, bestVideoUrl, mp3Match)
			}
			_ = ctx.React("✅")
			return nil
		}

		// ── MODE --view: player link/button ──
		return tiktokSendPlayer(ctx, title, bestVideoUrl, mp3Match)
	}

	if pageType == "profile" {
		titleRe := regexp.MustCompile(`<p class="text-title">([\s\S]*?)<\/p>`)
		linkRe := regexp.MustCompile(`(?i)<a class="pro-dl-link"[^>]*href="([^"]+)"`)
		titleMatches := titleRe.FindAllStringSubmatch(htmlData, -1)
		linkMatches := linkRe.FindAllStringSubmatch(htmlData, -1)

		if len(linkMatches) == 0 {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Tidak dapat menemukan video di profil ini.")
		}

		// ── MODE DEFAULT: kirim beberapa video terbaru sebagai SATU album ──
		if !wantView {
			maxSend := 4
			if len(linkMatches) < maxSend {
				maxSend = len(linkMatches)
			}

			// Unduh dulu semua video lalu kirim sebagai album (carousel).
			var items []albumItem
			firstTitle := ""
			for i := 0; i < maxSend; i++ {
				data, err := downloadTikTokBytes(linkMatches[i][1])
				if err != nil || len(data) == 0 {
					continue
				}
				if firstTitle == "" && i < len(titleMatches) {
					firstTitle = stripHTML(titleMatches[i][1])
				}
				items = append(items, albumItem{Data: data, IsVideo: true})
			}

			caption := fmt.Sprintf("🎬 *TikTok Profil* — %d video", len(items))
			if firstTitle != "" {
				caption += "\n📝 " + firstTitle
			}

			// ≥2 video → album. Bila album gagal / hanya 1 video → fallback per-item.
			if len(items) >= 2 {
				if err := sendMediaAlbum(ctx, items, caption); err == nil {
					_ = ctx.React("✅")
					return nil
				}
			}

			sent := 0
			for i := 0; i < maxSend; i++ {
				title := "Tanpa Judul"
				if i < len(titleMatches) {
					title = stripHTML(titleMatches[i][1])
				}
				cap := fmt.Sprintf("🎬 *Video %d/%d*\n📝 %s", i+1, maxSend, title)
				if err := sendTikTokVideo(ctx, linkMatches[i][1], cap); err == nil {
					sent++
				}
			}
			if sent == 0 {
				_ = ctx.React("❌")
				return ctx.Reply("❌ Gagal mengirim video dari profil ini.")
			}
			_ = ctx.React("✅")
			return nil
		}

		// ── MODE --view: daftar + tombol unduh (perilaku lama) ──
		maxDisplay := 5
		if len(linkMatches) < 5 {
			maxDisplay = len(linkMatches)
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("🗂️ *Mode:* Unduhan Profil\nHi *%s*, berikut %d video terbaru:\n\n", ctx.PushName, maxDisplay))
		for i := 0; i < maxDisplay; i++ {
			title := "Tanpa Judul"
			if i < len(titleMatches) {
				title = stripHTML(titleMatches[i][1])
			}
			sb.WriteString(fmt.Sprintf("*%d.* %s\n", i+1, title))
		}
		if len(linkMatches) > 5 {
			sb.WriteString(fmt.Sprintf("\n_... dan %d video lainnya disembunyikan._", len(linkMatches)-5))
		}

		btn := ctx.Button()
		btn.SetFooter("© Powered By TikTok")
		btn.SetTitle("🗂️ *TIKTOK PROFILE GENERATOR*")
		btn.SetBody(sb.String())
		if maxDisplay > 0 {
			btn.AddUrl("📹 Unduh Video 1", linkMatches[0][1], true)
		}
		if maxDisplay > 1 {
			btn.AddUrl("📹 Unduh Video 2", linkMatches[1][1], true)
		}
		return btn.SendToChat(ctx)
	}

	_ = ctx.React("❌")
	return ctx.Reply("❌ Tipe halaman TikTok tidak dikenali.")
}

// tiktokSendPlayer membangun pesan player link/button (mode --view) untuk satu video.
func tiktokSendPlayer(ctx *ContextBot, title, bestVideoUrl string, mp3Match []string) error {
	streamVideoUrl, err := ConvertToStreamURL(bestVideoUrl)
	if err != nil {
		streamVideoUrl = bestVideoUrl
	}
	finalVideo := fmt.Sprintf("https://ps.azumi.dev/watch?url=%s&type=video", url.QueryEscape(streamVideoUrl))

	var finalAudio string
	if len(mp3Match) > 1 {
		streamAudioUrl, err := ConvertToStreamURL(mp3Match[1])
		if err != nil {
			streamAudioUrl = mp3Match[1]
		}
		finalAudio = fmt.Sprintf("https://ps.azumi.dev/watch?url=%s&type=audio", url.QueryEscape(streamAudioUrl))
	}

	btn := ctx.Button()
	btn.SetFooter("© Powered By TikTok")
	btn.SetTitle("🎬 *TIKTOK MULTIMEDIA PLAYER*")
	btn.SetBody(fmt.Sprintf("Halo *%s*!\nVideo berhasil diproses.\n\n📝 *Deskripsi:*\n%s", ctx.PushName, title))
	btn.AddUrl("▶️ Putar Video", finalVideo, true)
	if finalAudio != "" {
		btn.AddUrl("🎵 Putar Audio", finalAudio, true)
	}
	return btn.SendToChat(ctx)
}

// sendTikTokVideo mengunduh video dari CDN lalu mengirimnya sebagai VideoMessage
// langsung ke chat (mengutip pesan pemicu).
func sendTikTokVideo(ctx *ContextBot, videoURL, caption string) error {
	data, err := downloadTikTokBytes(videoURL)
	if err != nil || len(data) == 0 {
		return fmt.Errorf("gagal mengunduh video: %v", err)
	}

	ctxUp, cancel := context.WithTimeout(ctx.Ctx, 60*time.Second)
	defer cancel()

	up, err := ctx.Client.Upload(ctxUp, data, whatsmeow.MediaVideo)
	if err != nil {
		return fmt.Errorf("gagal upload video: %v", err)
	}

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}

	msg := &waProto.Message{
		VideoMessage: &waProto.VideoMessage{
			Caption:       proto.String(caption),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			Mimetype:      proto.String("video/mp4"),
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			ContextInfo: &waProto.ContextInfo{
				StanzaID:      proto.String(ctx.Msg.Info.ID),
				Participant:   proto.String(senderStr),
				QuotedMessage: ctx.Msg.Message,
			},
		},
	}

	_, err = ctx.Client.SendMessage(ctx.Ctx, ctx.ChatJID.ToNonAD(), msg, src.AndroidExtra())
	return err
}

// downloadTikTokBytes mengunduh file dengan User-Agent browser (CDN TikTok
// kadang menolak request tanpa UA).
func downloadTikTokBytes(rawURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Mobile Safari/537.36")
	req.Header.Set("Referer", "https://www.tiktok.com/")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status unduhan %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// --- HELPER: Mendecode Token SnapCDN & Mengubah Parameter dl=1 Menjadi dl=0 ---
func ConvertToStreamURL(cdnURL string) (string, error) {
	u, err := url.Parse(cdnURL)
	if err != nil {
		return "", err
	}

	token := u.Query().Get("token")
	if token == "" {
		return "", fmt.Errorf("token tidak ditemukan")
	}

	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("format JWT tidak valid")
	}

	decodedPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("gagal decode payload: %v", err)
	}

	var data struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(decodedPayload, &data); err != nil {
		return "", fmt.Errorf("gagal unmarshal payload: %v", err)
	}

	realURL, err := url.Parse(data.URL)
	if err != nil {
		return data.URL, nil
	}

	query := realURL.Query()
	query.Set("dl", "0")
	realURL.RawQuery = query.Encode()

	return realURL.String(), nil
}
