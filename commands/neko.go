package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"bot-go/src"
)

// =================================================================
// COMMAND: NEKO (PREMIUM) — foto neko/catgirl anime acak dari nekosia.cat
//
// Ditandai `Premium: true` → hanya user premium (owner selalu) bisa memakainya,
// dan di grup hasilnya HANYA terlihat oleh member premium (sistem exclude).
// Ini contoh fitur premium: cukup set `Premium: true` + kirim hasil lewat
// ctx.PremiumImage / ctx.Reply (di-route otomatis).
// =================================================================

const nekosiaCatgirlAPI = "https://api.nekosia.cat/api/v1/images/catgirl"

type nekosiaResponse struct {
	Success bool `json:"success"`
	Image   struct {
		Original struct {
			URL string `json:"url"`
		} `json:"original"`
		Compressed struct {
			URL string `json:"url"`
		} `json:"compressed"`
	} `json:"image"`
	Tags   []string `json:"tags"`
	Source struct {
		URL string `json:"url"`
	} `json:"source"`
	Attribution struct {
		Artist struct {
			Username string `json:"username"`
			Profile  string `json:"profile"`
		} `json:"artist"`
	} `json:"attribution"`
}

func init() {
	RegisterCommand(Command{
		Name:        "Neko",
		Category:    "Premium",
		Aliases:     []string{"neko", "catgirl", "nekocat"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:neko|catgirl|nekocat)\s*$`),
		Description: "[Premium] Foto neko/catgirl anime acak",
		Premium:     true,
		Execute:     ExecuteNeko,
	})
}

func ExecuteNeko(ctx *ContextBot) error {
	_ = ctx.React("🐱")

	data, err := fetchNekosia()
	if err != nil {
		return ctx.Reply("❌ Gagal mengambil gambar neko.\n_" + err.Error() + "_")
	}

	// Ambil gambar (pilih compressed dulu, fallback original).
	imgURL := data.Image.Compressed.URL
	if imgURL == "" {
		imgURL = data.Image.Original.URL
	}
	if imgURL == "" {
		return ctx.Reply("❌ Respons API tak berisi gambar.")
	}
	img, mime, err := downloadBytes(imgURL)
	if err != nil {
		return ctx.Reply("❌ Gagal mengunduh gambar neko.\n_" + err.Error() + "_")
	}

	caption := buildNekoCaption(data)
	if err := ctx.PremiumImage(img, mime, caption); err != nil {
		return ctx.Reply("❌ Gagal mengirim gambar.\n_" + err.Error() + "_")
	}
	return nil
}

func fetchNekosia() (*nekosiaResponse, error) {
	resp, err := src.APIClient.Get(nekosiaCatgirlAPI)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out nekosiaResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("json tidak valid")
	}
	if !out.Success {
		return nil, fmt.Errorf("API mengembalikan gagal")
	}
	return &out, nil
}

func downloadBytes(url string) ([]byte, string, error) {
	resp, err := src.MediaClient.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" || !strings.HasPrefix(mime, "image/") {
		mime = "image/jpeg"
	}
	return data, mime, nil
}

func buildNekoCaption(d *nekosiaResponse) string {
	var b strings.Builder
	b.WriteString("🐾 *Neko / Catgirl*")
	if len(d.Tags) > 0 {
		tags := d.Tags
		if len(tags) > 6 {
			tags = tags[:6]
		}
		b.WriteString("\n🏷️ " + strings.Join(tags, ", "))
	}
	if d.Attribution.Artist.Username != "" {
		b.WriteString("\n🎨 " + d.Attribution.Artist.Username)
	}
	if d.Source.URL != "" {
		b.WriteString("\n🔗 " + d.Source.URL)
	}
	return b.String()
}
