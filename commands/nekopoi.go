package commands

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"bot-go/src"

	"github.com/PuerkitoBio/goquery"
)

// =================================================================
// FITUR PREMIUM: NEKOPOI (hentai) — port scraper JS → Go
//
// UX mirip `samehada`/`doujin`:
//   • `nekopoi`            → kirim VIDEO TERBARU (episode paling atas home).
//   • `nekopoi <kata>`     → cari → daftar kartu → balas NOMOR → halaman video
//                            (button "Tonton" + daftar link unduh).
//
// `Premium: true` → hanya premium/owner; di grup output disembunyikan dari
// non-premium (sistem exclude). Interaksi lanjutan lewat reply-router memakai
// pengiriman premium eksplisit (SendPremiumToChatWithID).
//
// Situs nekopoi.care diblokir ISP (DNS + SNI/DPI) & sertifikatnya kadaluarsa;
// semua request HTML/gambar lewat src.NekoClient (lihat src/nekopoi_client.go)
// yang menembus blokir tanpa proxy eksternal.
// =================================================================

const nekopoiBase = "https://nekopoi.care"

// =============================================
// STRUCT DATA
// =============================================

type NekoEpisode struct {
	Title string
	URL   string
	Image string
	Date  string
}

type NekoSearchItem struct {
	Title string
	URL   string
	Image string
	Desc  string
}

type NekoDownload struct {
	Quality string   // mis. "720p"
	Name    string   // label lengkap dari situs
	Links   []string // "Host — url"
}

type NekoDetail struct {
	Title     string
	Image     string
	Date      string
	Views     string
	Streams   []string // URL iframe streaming
	Downloads []NekoDownload
}

// NekoSession = state daftar hasil pencarian (di-key ID pesan via reply_router).
type NekoSession struct {
	URLs []string // index+1 = nomor yang dibalas
}

// =============================================
// REGISTRASI COMMAND
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "Nekopoi",
		Category:    "Premium",
		Aliases:     []string{"nekopoi"},
		Pattern:     regexp.MustCompile(`(?i)^nekopoi(?:\s+([\s\S]+))?$`),
		Description: "[Premium] Nekopoi — tanpa argumen = video terbaru, dengan kata = cari",
		Premium:     true,
		Execute:     ExecuteNekopoi,
	})
}

// =============================================
// HELPER SCRAPING
// =============================================

var reBgImage = regexp.MustCompile(`url\(['"]?(.*?)['"]?\)`)

// bgImage menarik URL dari atribut style background-image.
func bgImage(style string) string {
	if m := reBgImage.FindStringSubmatch(style); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// reQuality mengambil "720p" dari label seperti "... [720p]".
var reQuality = regexp.MustCompile(`(?i)\[(\d+p|hd|sd|alternatif)\]`)

// parseNekoLatest mengambil daftar episode terbaru dari HTML home.
func parseNekoLatest(html []byte) ([]NekoEpisode, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return nil, err
	}
	var out []NekoEpisode
	doc.Find("#nk-episode-grid .nk-post-card").Each(func(_ int, s *goquery.Selection) {
		a := s.Find(".nk-post-meta h2 a").First()
		link, _ := a.Attr("href")
		title := strings.TrimSpace(a.Text())
		img := bgImage(s.Find(".nk-thumb-crop").First().AttrOr("style", ""))
		date := strings.TrimSpace(s.Find(".nk-post-meta span").First().Text())
		if link != "" && title != "" {
			out = append(out, NekoEpisode{Title: title, URL: link, Image: img, Date: date})
		}
	})
	return out, nil
}

// parseNekoSearch mengambil hasil pencarian dari HTML search.
func parseNekoSearch(html []byte) ([]NekoSearchItem, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return nil, err
	}
	var out []NekoSearchItem
	doc.Find("a.nk-search-item").Each(func(_ int, s *goquery.Selection) {
		link, _ := s.Attr("href")
		title := strings.TrimSpace(s.Find(".nk-search-info h2").First().Text())
		img := bgImage(s.Find(".nk-search-thumb").First().AttrOr("style", ""))
		desc := strings.TrimSpace(s.Find(".nk-search-desc").First().Text())
		if link != "" && title != "" {
			out = append(out, NekoSearchItem{Title: title, URL: link, Image: img, Desc: desc})
		}
	})
	return out, nil
}

// parseNekoDetail mengambil detail video (stream + link unduh) dari HTML detail.
func parseNekoDetail(html []byte) (*NekoDetail, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return nil, err
	}
	d := &NekoDetail{}
	d.Title = strings.TrimSpace(doc.Find(".nk-post-header h1").First().Text())
	d.Image, _ = doc.Find(".nk-featured-img img").First().Attr("src")
	doc.Find(".nk-post-header-meta span").Each(func(_ int, s *goquery.Selection) {
		txt := strings.TrimSpace(s.Text())
		if strings.Contains(txt, "kali") {
			d.Views = txt
		} else if d.Date == "" {
			d.Date = txt
		}
	})

	// Streaming (iframe embed).
	doc.Find(".nk-player-frame iframe").Each(func(_ int, s *goquery.Selection) {
		if src, ok := s.Attr("src"); ok && src != "" {
			d.Streams = append(d.Streams, src)
		}
	})

	// Link unduh, dikelompokkan per kualitas.
	doc.Find(".nk-download-row").Each(func(_ int, s *goquery.Selection) {
		name := strings.TrimSpace(s.Find(".nk-download-name").First().Text())
		dl := NekoDownload{Name: name}
		if m := reQuality.FindStringSubmatch(name); len(m) == 2 {
			dl.Quality = strings.ToLower(m[1])
		}
		s.Find(".nk-download-links a").Each(func(_ int, a *goquery.Selection) {
			href, _ := a.Attr("href")
			host := strings.TrimSpace(a.Text())
			if href != "" {
				if host == "" {
					host = "Link"
				}
				dl.Links = append(dl.Links, host+" → "+href)
			}
		})
		if len(dl.Links) > 0 {
			d.Downloads = append(d.Downloads, dl)
		}
	})

	if d.Title == "" {
		return nil, fmt.Errorf("struktur halaman tak dikenali")
	}
	return d, nil
}

// =============================================
// EXECUTE
// =============================================

func ExecuteNekopoi(ctx *ContextBot) error {
	query := strings.TrimSpace(ctx.Args)
	go func() { _ = ctx.React("⏳") }()

	if query == "" {
		return nekopoiLatest(ctx)
	}
	return nekopoiSearch(ctx, query)
}

// nekopoiLatest mengirim video TERBARU langsung (tanpa daftar).
func nekopoiLatest(ctx *ContextBot) error {
	html, err := src.NekoGet(nekopoiBase + "/")
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengakses Nekopoi.\n_" + err.Error() + "_")
	}
	eps, err := parseNekoLatest(html)
	if err != nil || len(eps) == 0 {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Tidak ada video terbaru yang bisa diambil.")
	}
	return sendNekoDetail(ctx, eps[0].URL, "🆕 *NEKOPOI — VIDEO TERBARU*")
}

// nekopoiSearch menampilkan daftar kartu hasil pencarian (samehada style).
func nekopoiSearch(ctx *ContextBot, query string) error {
	html, err := src.NekoGet(nekopoiBase + "/?s=" + url.QueryEscape(query) + "&post_type=anime")
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengakses Nekopoi.\n_" + err.Error() + "_")
	}
	items, err := parseNekoSearch(html)
	if err != nil || len(items) == 0 {
		_ = ctx.React("❌")
		return ctx.Reply(fmt.Sprintf("❌ Tidak ditemukan hasil untuk: %s", query))
	}

	session := &NekoSession{}
	rb := ctx.AIRich().
		SetTitle(fmt.Sprintf("🔞 Hasil Nekopoi: %s", strings.ToUpper(query))).
		SetFooter("Balas dengan NOMOR (1-10) untuk menonton")

	for i, it := range items {
		if i >= 10 {
			break
		}
		rb.AddText(fmt.Sprintf("*%d.* %s", i+1, it.Title))
		rb.AddProduct(src.AIProduct{
			Title:    it.Title,
			Brand:    "Nekopoi",
			Price:    "🔞 Hentai",
			ImageURL: it.Image,
		})
		session.URLs = append(session.URLs, it.URL)
	}

	msgID, err := rb.SendPremiumToChatWithID(ctx)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal menampilkan hasil.")
	}
	replyRouter.Register(msgID, "nekopoi", session)
	_ = ctx.React("✅")
	return nil
}

// =============================================
// INTERAKSI (via reply_router)
// =============================================

func handleNekopoiReply(ctx *ContextBot, rc *ReplyContext) error {
	session, ok := rc.Data.(*NekoSession)
	if !ok {
		return nil
	}
	idx, err := strconv.Atoi(strings.TrimSpace(ctx.TextMessage))
	if err != nil {
		return nil
	}
	if idx < 1 || idx > len(session.URLs) {
		return nil
	}
	return sendNekoDetail(ctx, session.URLs[idx-1], "🔞 *NEKOPOI*")
}

// sendNekoDetail memuat halaman video lalu mengirim button (poster + stream +
// daftar link unduh) secara premium.
func sendNekoDetail(ctx *ContextBot, pageURL, heading string) error {
	go func() { _ = ctx.React("⏳") }()

	html, err := src.NekoGet(pageURL)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memuat halaman video.\n_" + err.Error() + "_")
	}
	d, err := parseNekoDetail(html)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memproses data video.")
	}

	// Susun caption.
	var sb strings.Builder
	sb.WriteString(heading + "\n\n")
	sb.WriteString("🎬 *" + d.Title + "*\n")
	if d.Date != "" {
		sb.WriteString("📅 " + d.Date + "\n")
	}
	if d.Views != "" {
		sb.WriteString("👁️ " + d.Views + "\n")
	}
	if len(d.Downloads) > 0 {
		sb.WriteString("\n*⬇️ Link Unduh:*\n")
		for _, dl := range d.Downloads {
			label := dl.Quality
			if label == "" {
				label = dl.Name
			}
			sb.WriteString("\n*" + strings.ToUpper(label) + "*\n")
			for _, l := range dl.Links {
				sb.WriteString("• " + l + "\n")
			}
		}
	}
	if len(d.Streams) == 0 && len(d.Downloads) == 0 {
		sb.WriteString("\n_⚠️ Sumber stream/unduh belum tersedia untuk video ini._")
	}

	btn := ctx.Button()
	btn.SetTitle("🔞 *NEKOPOI PLAYER*")
	btn.SetBody(sb.String())
	btn.SetFooter("© Powered By Nekopoi")

	// Poster sebagai header (diunduh lewat NekoClient karena ikut terblokir).
	if d.Image != "" {
		if img, _, e := src.NekoDownload(d.Image); e == nil && len(img) > 0 {
			btn.SetImage(img)
		}
	}

	// Tombol streaming (maksimal 3 server).
	for i, s := range d.Streams {
		if i >= 3 {
			break
		}
		btn.AddUrl(fmt.Sprintf("▶️ Tonton Server %d", i+1), s, true)
	}

	if _, err := btn.SendPremiumToChatWithID(ctx); err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal mengirim video.\n_" + err.Error() + "_")
	}
	_ = ctx.React("✅")
	return nil
}
