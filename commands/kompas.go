package commands // Sesuaikan dengan nama package kamu

import (
	"encoding/json"
	"fmt"
	"io"
	// "net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src"
)

// =============================================
// 1. STATE DATA (di-key oleh ID pesan via reply_router)
// =============================================

// KompasSessionData menyimpan state khusus untuk interaksi Berita Kompas
type KompasSessionData struct {
	NewsURLs  []string
	State     string    // "list"
	ExpiresAt time.Time // tidak dipakai lagi (expiry ditangani reply_router)
}

// =============================================
// 2. JSON STRUCT DEFINITIONS
// =============================================

// Struct untuk API 1: Tanpa Query (Trending/Lainnya)
type KompasTrendingRes struct {
	Success bool `json:"success"`
	Result  []struct {
		Title       string `json:"title"`
		Link        string `json:"link"`
		Category    string `json:"category"`
		PublishTime string `json:"publish_time"`
		ImageURL    string `json:"image_url"`
	} `json:"result"`
}

// Struct untuk API 2: Dengan Query (Pencarian)
type KompasSearchRes struct {
	Success bool `json:"success"`
	Result  []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Category      string `json:"category"`
		PublishedDate string `json:"published_date"`
		ImageURL      string `json:"image_url"`
	} `json:"result"`
}

// Struct untuk API 3: Detail Berita
type KompasDetailRes struct {
	Success bool `json:"success"`
	Result  struct {
		URL          string `json:"url"`
		Title        string `json:"title"`
		Category     string `json:"category"`
		Subcategory  string `json:"subcategory"`
		Author       string `json:"author"`
		PublishDate  string `json:"publish_date"`
		Tags         string `json:"tags"`
		MainImage    string `json:"main_image"`
		ImageCaption string `json:"image_caption"`
		Content      string `json:"content"`
	} `json:"result"`
}

// =============================================
// 3. COMMAND REGISTRATIONS
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "Kompas News",
		Category:    "Finder",
		Aliases:     []string{"kompas", "berita"},
		Pattern:     regexp.MustCompile(`(?i)^(?:kompas|berita)(?:\s+(.+))?$`), // Regex opsional untuk query
		Description: "Membaca berita terbaru atau mencari berita dari Kompas",
		Execute:     ExecuteKompasSearch,
	})
}

// =============================================
// 4. PHASE 1: EXECUTE KOMPAS SEARCH (API 1 & 2)
// =============================================

func ExecuteKompasSearch(ctx *ContextBot) error {
	query := strings.TrimSpace(ctx.Args)

	go func() { _ = ctx.React("⏳") }()
	newSession := &KompasSessionData{
		State: "list",
	}

	// Tampilan kartu AIRich (dengan gambar) — konsisten dengan `play --all`.
	rb := src.NewAIRich().SetFooter("Balas dengan NOMOR (1-10) untuk membaca detail berita")

	// Cabang 1: Jika user TIDAK memberikan query (Ambil Trending)
	if query == "" {
		apiUrl := "https://ps.azumi.dev/d/other/kompas"
		resp, err := httpClient.Get(apiUrl)
		if err != nil {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Gagal terhubung ke API Kompas.")
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		var data KompasTrendingRes
		if err := json.Unmarshal(body, &data); err != nil || !data.Success || len(data.Result) == 0 {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Tidak ada berita terkini yang ditemukan.")
		}

		rb.SetTitle("📰 Berita Terkini Kompas")
		for i, news := range data.Result {
			if i >= 10 {
				break
			}
			rb.AddText(fmt.Sprintf("*%d.* %s", i+1, news.Title))
			rb.AddProduct(src.AIProduct{
				Title:      news.Title,
				Brand:      news.Category,
				Price:      news.PublishTime,
				ProductURL: news.Link,
				ImageURL:   news.ImageURL,
			})
			newSession.NewsURLs = append(newSession.NewsURLs, news.Link)
		}

		// Cabang 2: Jika user MEMBERIKAN query (Ambil Pencarian)
	} else {
		apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/finder/kompas?q=%s", url.QueryEscape(query))
		resp, err := httpClient.Get(apiUrl)
		if err != nil {
			_ = ctx.React("❌")
			return ctx.Reply("❌ Gagal terhubung ke API Kompas.")
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		var data KompasSearchRes
		if err := json.Unmarshal(body, &data); err != nil || !data.Success || len(data.Result) == 0 {
			_ = ctx.React("❌")
			return ctx.Reply(fmt.Sprintf("❌ Tidak ditemukan berita untuk: %s", query))
		}

		rb.SetTitle(fmt.Sprintf("🔍 Hasil Pencarian Berita: %s", strings.ToUpper(query)))
		for i, news := range data.Result {
			if i >= 10 {
				break
			}
			rb.AddText(fmt.Sprintf("*%d.* %s", i+1, news.Title))
			rb.AddProduct(src.AIProduct{
				Title:      news.Title,
				Brand:      news.Category,
				Price:      news.PublishedDate,
				ProductURL: news.URL,
				ImageURL:   news.ImageURL,
			})
			newSession.NewsURLs = append(newSession.NewsURLs, news.URL)
		}
	}

	// Kirim list & daftarkan ID pesan ke reply-router
	msgID, err := rb.SendToChatWithID(ctx)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal menampilkan daftar berita.")
	}
	replyRouter.Register(msgID, "kompas", newSession)
	_ = ctx.React("✅")
	return nil
}

// =============================================
// 5. PHASE 2: INTERACTIVE HANDLER (via reply_router)
// =============================================

func handleKompasReply(ctx *ContextBot, rc *ReplyContext) error {
	session, ok := rc.Data.(*KompasSessionData)
	if !ok {
		return nil
	}

	textRaw := strings.TrimSpace(ctx.TextMessage)
	index, err := strconv.Atoi(textRaw)
	if err != nil {
		return nil
	}

	// Memilih Berita dari List
	if session.State == "list" {
		if index < 1 || index > len(session.NewsURLs) {
			return nil // Di luar jangkauan list, abaikan diam-diam
		}
		selectedURL := session.NewsURLs[index-1]
		return loadKompasDetail(ctx, selectedURL)
	}

	return nil
}

func loadKompasDetail(ctx *ContextBot, newsURL string) error {
	go func() { _ = ctx.React("⏳") }()
	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/fetcher/kompas?url=%s", url.QueryEscape(newsURL))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		return ctx.Reply("❌ Gagal memuat detail berita.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data KompasDetailRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success {
		return ctx.Reply("❌ Terjadi kesalahan pada sistem server.")
	}

	result := data.Result

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📄 *%s*\n\n", strings.ToUpper(result.Title)))
	sb.WriteString(fmt.Sprintf("📌 Kategori : %s / %s\n", result.Category, result.Subcategory))
	sb.WriteString(fmt.Sprintf("✍️ Penulis  : %s\n", result.Author))
	sb.WriteString(fmt.Sprintf("📅 Rilis    : %s\n\n", result.PublishDate))

	// Format konten berita agar lebih rapi
	content := result.Content
	content = strings.ReplaceAll(content, "&quot;", "\"")

	sb.WriteString(fmt.Sprintf("%s\n\n", content))

	if result.Tags != "" {
		sb.WriteString(fmt.Sprintf("🏷️ *Tags:* _%s_\n", result.Tags))
	}
	sb.WriteString(fmt.Sprintf("🔗 *Sumber:* %s", result.URL))

	return ctx.Reply(sb.String())
}
