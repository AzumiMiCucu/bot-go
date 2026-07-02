package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"bot-go/src"
)

// =============================================
// FITUR PREMIUM: BACA MANHWA/DOUJIN (mirip Shinigami)
//
// UX identik `shinigami`: list kartu AIRich → balas NOMOR → detail + daftar
// chapter → balas nomor chapter → Button "Mulai Membaca" (+ Prev/Next).
//
// BEDA: fitur ini `Premium: true` → hanya premium/owner boleh memicu, DAN di grup
// seluruh output (list/detail/button) DISEMBUNYIKAN dari non-premium lewat sistem
// exclude. Karena interaksi lanjutan lewat reply-router berjalan SEBELUM gate
// premium (matchedCommand==nil → auto-hide non-aktif), setiap langkah memakai
// pengiriman premium secara EKSPLISIT (SendPremiumToChatWithID / SendPremiumText).
//
// Sumber: ps.azumi.dev (desu.xxx). Reader: https://ps.azumi.dev/comic?doujin=<chapterID>
// =============================================

// =============================================
// STATE (di-key oleh ID pesan via reply_router)
// =============================================

type DoujinSession struct {
	Slugs       []string          // urut sesuai list (index+1 = nomor yang dibalas)
	ChapterIDs  map[string]string // nomor-chapter → chapter_id
	ChNumbers   []string          // urut chapter (terbaru → terlama)
	PrevChapNum string
	NextChapNum string
	Title       string
	State       string // "list" | "detail"
}

// =============================================
// JSON STRUCT
// =============================================

type doujinGenre struct {
	Genres struct {
		Name string `json:"name"`
	} `json:"genres"`
}

// DoujinListRes dipakai untuk latest (misc) & search (finder) — `result` array.
type DoujinListRes struct {
	Success bool `json:"success"`
	Result  []struct {
		ID       string        `json:"id"`
		Title    string        `json:"title"`
		Slug     string        `json:"slug"`
		CoverURL string        `json:"cover_url"`
		Status   string        `json:"status"`
		Type     string        `json:"type"`
		Rating   float64       `json:"rating"`
		Views    int           `json:"views"`
		Genres   []doujinGenre `json:"manga_genres"`
	} `json:"result"`
}

// DoujinDetailRes dipakai untuk fetcher — `result` objek.
type DoujinDetailRes struct {
	Success bool `json:"success"`
	Result  struct {
		Title       string        `json:"title"`
		Description string        `json:"description"`
		Status      string        `json:"status"`
		Type        string        `json:"type"`
		Rating      float64       `json:"rating"`
		AltTitles   string        `json:"alt_titles"`
		Genres      []doujinGenre `json:"manga_genres"`
		Chapters    []struct {
			ID            string  `json:"id"`
			ChapterNumber float64 `json:"chapter_number"`
			Title         string  `json:"title"`
		} `json:"chapters"`
	} `json:"result"`
}

// =============================================
// REGISTRASI COMMAND
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "Doujin Reader",
		Category:    "Premium",
		Aliases:     []string{"doujin"},
		Pattern:     regexp.MustCompile(`(?i)^doujin(?:\s+(.+))?$`),
		Description: "[Premium] Baca manhwa/doujin — tanpa argumen = terbaru, dengan kata = cari",
		Premium:     true,
		Execute:     ExecuteDoujinSearch,
	})
}

// =============================================
// HANDLER
// =============================================

func ExecuteDoujinSearch(ctx *ContextBot) error {
	query := strings.TrimSpace(ctx.Args)

	go func() { _ = ctx.React("⏳") }()

	var apiURL, heading string
	if query == "" {
		apiURL = "https://ps.azumi.dev/d/misc/doujin"
		heading = "🆕 Doujin/Manhwa Terbaru"
	} else {
		apiURL = fmt.Sprintf("https://ps.azumi.dev/d/finder/doujin?q=%s", url.QueryEscape(query))
		heading = fmt.Sprintf("🔍 Hasil Pencarian: %s", strings.ToUpper(query))
	}

	resp, err := httpClient.Get(apiURL)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal terhubung ke API Doujin.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data DoujinListRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success || len(data.Result) == 0 {
		_ = ctx.React("❌")
		if query == "" {
			return ctx.Reply("❌ Tidak ada data terbaru saat ini.")
		}
		return ctx.Reply(fmt.Sprintf("❌ Tidak ditemukan hasil untuk: %s", query))
	}

	session := &DoujinSession{State: "list"}

	rb := ctx.AIRich().
		SetTitle(heading).
		SetFooter("Balas dengan NOMOR (1-10) untuk melihat detail")

	for i, item := range data.Result {
		if i >= 10 {
			break
		}
		typ := item.Type
		if typ == "" {
			typ = "Doujin"
		}
		rb.AddText(fmt.Sprintf("*%d.* %s", i+1, item.Title))
		rb.AddProduct(src.AIProduct{
			Title:    item.Title,
			Brand:    strings.Title(typ),
			Price:    fmt.Sprintf("⭐ %.1f", item.Rating),
			ImageURL: item.CoverURL,
		})
		session.Slugs = append(session.Slugs, item.Slug)
	}

	msgID, err := rb.SendPremiumToChatWithID(ctx)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal menampilkan daftar doujin.")
	}
	replyRouter.Register(msgID, "doujin", session)
	_ = ctx.React("✅")
	return nil
}

// =============================================
// INTERAKSI (via reply_router)
// =============================================

func handleDoujinReply(ctx *ContextBot, rc *ReplyContext) error {
	session, ok := rc.Data.(*DoujinSession)
	if !ok {
		return nil
	}

	textRaw := strings.ToLower(strings.TrimSpace(ctx.TextMessage))

	// A. Navigasi Prev/Next
	if textRaw == "before" || textRaw == "after" {
		targetChapNum := session.PrevChapNum
		if textRaw == "after" {
			targetChapNum = session.NextChapNum
		}
		if targetChapNum == "" {
			return ctx.Reply("❌ Tautan navigasi tidak tersedia.")
		}
		chapterID, exists := session.ChapterIDs[targetChapNum]
		if !exists {
			return ctx.Reply("❌ Data chapter tidak ditemukan.")
		}
		return sendDoujinChapterButton(ctx, chapterID, targetChapNum, session)
	}

	// B. Angka murni
	index, err := strconv.Atoi(textRaw)
	if err != nil {
		return nil
	}

	if session.State == "list" {
		if index < 1 || index > len(session.Slugs) {
			return nil
		}
		return loadDoujinDetail(ctx, session.Slugs[index-1])
	}

	if session.State == "detail" {
		chapterID, exists := session.ChapterIDs[textRaw]
		if !exists {
			return nil
		}
		return sendDoujinChapterButton(ctx, chapterID, textRaw, session)
	}

	return nil
}

func loadDoujinDetail(ctx *ContextBot, slug string) error {
	go func() { _ = ctx.React("⏳") }()
	apiURL := fmt.Sprintf("https://ps.azumi.dev/d/fetcher/doujin?slug=%s", url.QueryEscape(slug))
	resp, err := httpClient.Get(apiURL)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memuat detail doujin.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data DoujinDetailRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Terjadi kesalahan pada sistem server.")
	}

	info := data.Result
	session := &DoujinSession{
		State:      "detail",
		ChapterIDs: make(map[string]string),
		Title:      info.Title,
	}

	var genres []string
	for _, g := range info.Genres {
		if g.Genres.Name != "" {
			genres = append(genres, g.Genres.Name)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📄 *Detail: %s*\n\n", strings.ToUpper(info.Title)))
	sb.WriteString(fmt.Sprintf("⭐ Rating : %.1f\n", info.Rating))
	if info.Type != "" {
		sb.WriteString(fmt.Sprintf("📌 Tipe   : %s\n", strings.Title(info.Type)))
	}
	if info.Status != "" {
		sb.WriteString(fmt.Sprintf("📶 Status : %s\n", strings.Title(info.Status)))
	}
	if len(genres) > 0 {
		sb.WriteString(fmt.Sprintf("🎭 Genre  : %s\n", strings.Join(genres, ", ")))
	}
	sb.WriteString("\n")

	if syn := strings.TrimSpace(info.Description); syn != "" {
		if len(syn) > 250 {
			syn = responTruncate(syn, 250)
		}
		sb.WriteString(fmt.Sprintf("*Sinopsis:*\n_%s_\n\n", syn))
	}
	sb.WriteString("*Daftar Chapter:*\n")

	for _, ch := range info.Chapters {
		chNum := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", ch.ChapterNumber), "0"), ".")
		sb.WriteString(fmt.Sprintf("- Chapter %s\n", chNum))
		session.ChapterIDs[chNum] = ch.ID
		session.ChNumbers = append(session.ChNumbers, chNum)
	}

	sb.WriteString("---\nBalas pesan ini dengan angka chapter *(Contoh: 1)* untuk mulai membaca.")

	// Kirim premium (di grup tetap ter-exclude walau dari jalur reply-router).
	msgID, err := src.SendPremiumText(ctx.Client, ctx.ChatJID, sb.String())
	if err != nil {
		_ = ctx.React("❌")
		return err
	}
	replyRouter.Register(msgID, "doujin", session)
	_ = ctx.React("✅")
	return nil
}

func sendDoujinChapterButton(ctx *ContextBot, chapterID, chapterNum string, base *DoujinSession) error {
	go func() { _ = ctx.React("⏳") }()
	readURL := fmt.Sprintf("https://ps.azumi.dev/comic?doujin=%s", chapterID)

	navSession := &DoujinSession{
		State:      "detail",
		ChapterIDs: base.ChapterIDs,
		ChNumbers:  base.ChNumbers,
		Title:      base.Title,
	}
	updateDoujinNav(navSession, chapterNum)

	btn := ctx.Button()
	btn.SetTitle("📖 *DOUJIN READER*")
	btn.SetBody(fmt.Sprintf("🎬 *%s*\n\nChapter %s siap dibaca. Pilih opsi di bawah.", navSession.Title, chapterNum))
	btn.SetFooter("© Powered By Azumi")
	btn.AddUrl("Mulai Membaca", readURL, true)

	if navSession.PrevChapNum != "" {
		btn.AddReply("Previous Chapter", "before")
	}
	if navSession.NextChapNum != "" {
		btn.AddReply("Next Chapter", "after")
	}

	msgID, err := btn.SendPremiumToChatWithID(ctx)
	if err != nil {
		_ = ctx.React("❌")
		return err
	}
	replyRouter.Register(msgID, "doujin", navSession)
	_ = ctx.React("✅")
	return nil
}

// updateDoujinNav mengisi Prev/NextChapNum berdasarkan posisi chapterNum di daftar
// (asumsi daftar terbaru→terlama: index kecil = chapter terbaru).
func updateDoujinNav(session *DoujinSession, currentChapNum string) {
	session.PrevChapNum = ""
	session.NextChapNum = ""

	idx := -1
	for i, num := range session.ChNumbers {
		if num == currentChapNum {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	if idx > 0 {
		session.NextChapNum = session.ChNumbers[idx-1]
	}
	if idx < len(session.ChNumbers)-1 {
		session.PrevChapNum = session.ChNumbers[idx+1]
	}
}
