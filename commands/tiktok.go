package commands

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

func init() {
	RegisterCommand(Command{
		Name:        "TikTok Downloader",
		Category:    "Downloader",
		Aliases:     []string{"tt", "ttdl", "tiktok"},
		Pattern:     regexp.MustCompile(`(?i)^(?:tiktok|ttdl|tt)\s+(.+)`),
		Description: "Unduh video TikTok (Mendukung Profil & Single Video)",
		Price:       0.015,
		Execute:     ExecuteTikTok,
	})
}

func ExecuteTikTok(ctx *ContextBot) error {
	re := regexp.MustCompile(`(?i)(https?:\/\/(?:www\.|vt\.)?tiktok\.com\/[^\s]+)`)
	matches := re.FindStringSubmatch(ctx.TextMessage)
	
	if len(matches) == 0 {
		return ctx.Reply("⚠️ Tidak menemukan URL TikTok yang valid.")
	}

	targetUrl := matches[1]
	_ = ctx.React("⏳")

	data := url.Values{}
	data.Set("q", targetUrl)
	data.Set("cursor", "0")
	data.Set("page", "0")
	data.Set("lang", "en")

	req, _ := http.NewRequest("POST", "https://savetikpro.com/api/ajaxSearch", strings.NewReader(data.Encode()))
	req.Header.Add("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("referer", "https://savetikpro.com/")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return ctx.Reply("❌ Gagal terhubung ke server.")
	}
	defer resp.Body.Close()

	bodyText, _ := io.ReadAll(resp.Body)
	
	var result map[string]interface{}
	json.Unmarshal(bodyText, &result)

	if result["status"] != "ok" || result["data"] == nil {
		return ctx.Reply("❌ Gagal menemukan data. Pastikan URL benar dan tidak privat.")
	}

	htmlData := result["data"].(string)
	pageType := result["pageType"].(string)

	cleanText := func(str string) string {
		reHTML := regexp.MustCompile(`<[^>]*>?`)
		cleaned := reHTML.ReplaceAllString(str, "")
		return strings.TrimSpace(cleaned)
	}

	// Inisialisasi komponen Button
	btn := ctx.Button()
	btn.SetFooter("© Powered By TikTok")

	if pageType == "detail" {
		titleRe := regexp.MustCompile(`<h3>([\s\S]*?)<\/h3>`)
		titleMatch := titleRe.FindStringSubmatch(htmlData)
		title := "Tanpa Judul"
		if len(titleMatch) > 1 {
			title = cleanText(titleMatch[1])
		}

		mp4Re := regexp.MustCompile(`(?i)<a[^>]*href="([^"]+)"[^>]*>.*?Download MP4.*?<\/a>`)
		mp4Matches := mp4Re.FindAllStringSubmatch(htmlData, -1)

		mp3Re := regexp.MustCompile(`(?i)<a[^>]*href="([^"]+)"[^>]*>.*?Download MP3.*?<\/a>`)
		mp3Match := mp3Re.FindStringSubmatch(htmlData)

		if len(mp4Matches) == 0 {
			return ctx.Reply("❌ Tidak dapat menemukan tautan video pada halaman ini.")
		}

		bestVideoUrl := mp4Matches[0][1]
		// Biasanya index ke-2 adalah kualitas HD/terbaik jika tersedia 3 opsi
		if len(mp4Matches) >= 3 {
			bestVideoUrl = mp4Matches[2][1]
		}

		// --- 1. PROSES VIDEO ---
		streamVideoUrl, err := ConvertToStreamURL(bestVideoUrl)
		if err != nil {
			streamVideoUrl = bestVideoUrl // Fallback
		}
		// WAJIB QueryEscape agar parameter link streaming tidak bertabrakan dengan ashema.my.id
		finalVideo := fmt.Sprintf("https://ashema.my.id/watch?url=%s&type=video", url.QueryEscape(streamVideoUrl))

		// --- 2. PROSES AUDIO (AMANKAN DARI PANIC) ---
		var finalAudio string
		if len(mp3Match) > 1 {
			streamAudioUrl, err := ConvertToStreamURL(mp3Match[1])
			if err != nil {
				streamAudioUrl = mp3Match[1] // Fallback
			}
			finalAudio = fmt.Sprintf("https://ashema.my.id/watch?url=%s&type=audio", url.QueryEscape(streamAudioUrl))
		}

		// Set Tampilan Button
		btn.SetTitle("🎬 *TIKTOK MULTIMEDIA PLAYER*")
		btn.SetBody(fmt.Sprintf("Halo *%s*!\nVideo berhasil diproses.\n\n📝 *Deskripsi:*\n%s", ctx.PushName, title))
		
		btn.AddUrl("▶️ Putar Video", finalVideo, true)
		
		// Hanya tambahkan tombol audio jika benar-benar ada
		if finalAudio != "" {
			btn.AddUrl("🎵 Putar Audio", finalAudio, true)
		}

	} else if pageType == "profile" {
		titleRe := regexp.MustCompile(`<p class="text-title">([\s\S]*?)<\/p>`)
		linkRe := regexp.MustCompile(`(?i)<a class="pro-dl-link"[^>]*href="([^"]+)"`)

		titleMatches := titleRe.FindAllStringSubmatch(htmlData, -1)
		linkMatches := linkRe.FindAllStringSubmatch(htmlData, -1)

		if len(linkMatches) == 0 {
			return ctx.Reply("❌ Tidak dapat menemukan video di profil ini.")
		}

		maxDisplay := 5
		if len(linkMatches) < 5 {
			maxDisplay = len(linkMatches)
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📂 *Mode:* Unduhan Profil\nHi *%s*, berikut %d video terbaru:\n\n", ctx.PushName, maxDisplay))

		for i := 0; i < maxDisplay; i++ {
			title := "Tanpa Judul"
			if i < len(titleMatches) {
				title = cleanText(titleMatches[i][1])
			}
			sb.WriteString(fmt.Sprintf("*%d.* %s\n", i+1, title))
		}

		if len(linkMatches) > 5 {
			sb.WriteString(fmt.Sprintf("\n_... dan %d video lainnya disembunyikan._", len(linkMatches)-5))
		}

		btn.SetTitle("🗂️ *TIKTOK PROFILE GENERATOR*")
		btn.SetBody(sb.String())

		// Batasan WhatsApp: sematkan tautan unduhan 2 video teratas saja
		if maxDisplay > 0 {
			btn.AddUrl("📹 Unduh Video 1", linkMatches[0][1], true)
		}
		if maxDisplay > 1 {
			btn.AddUrl("📹 Unduh Video 2", linkMatches[1][1], true)
		}
	}
	
	return btn.SendToChat(ctx)
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