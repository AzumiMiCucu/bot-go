package commands // Sesuaikan dengan nama package kamu

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

// SessionData menyimpan state khusus untuk interaksi Shinigami
type SessionData struct {
	MangaIDs    []string
	ChapterIDs  map[string]string
	ChNumbers   []string
	PrevChapNum string
	NextChapNum string
	MangaTitle  string
	State       string    // "list" atau "detail"
	ExpiresAt   time.Time // tidak dipakai lagi (expiry ditangani reply_router)
}

// httpClient dipakai bersama oleh seluruh fitur Finder di package ini.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// =============================================
// 2. JSON STRUCT DEFINITIONS
// =============================================

type ShinigamiSearchRes struct {
	Success bool `json:"success"`
	Result  struct {
		Data []struct {
			Title    string  `json:"title"`
			MangaID  string  `json:"manga_id"`
			UserRate float64 `json:"user_rate"`
			Taxonomy struct {
				Format []struct {
					Name string `json:"name"`
				} `json:"Format"`
			} `json:"taxonomy"`
		} `json:"data"`
	} `json:"result"`
}

type ShinigamiDetailRes struct {
	Success bool `json:"success"`
	Result  struct {
		Success bool `json:"success"`
		Data    struct {
			Info struct {
				Title       string  `json:"title"`
				Description string  `json:"description"`
				UserRate    float64 `json:"user_rate"`
				Taxonomy    struct {
					Format []struct {
						Name string `json:"name"`
					} `json:"Format"`
					Genre []struct {
						Name string `json:"name"`
					} `json:"Genre"`
				} `json:"taxonomy"`
			} `json:"info"`
			Chapters []struct {
				ChapterID     string  `json:"chapter_id"`
				ChapterNumber float64 `json:"chapter_number"`
			} `json:"chapters"`
		} `json:"data"`
	} `json:"result"`
}

// =============================================
// 3. COMMAND REGISTRATIONS
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "Shinigami Search",
		Category:    "Finder",
		Aliases:     []string{"manhwa", "shinigami"},
		Pattern:     regexp.MustCompile(`(?i)^(?:manhwa|shinigami)\s+(.+)`),
		Description: "Mencari komik dari Shinigami",
		Execute:     ExecuteShinigamiSearch,
	})
}

// =============================================
// 4. HANDLER IMPLEMENTATIONS
// =============================================

func ExecuteShinigamiSearch(ctx *ContextBot) error {
	query := strings.TrimSpace(ctx.Args)
	if query == "" {
		return ctx.Reply("⚠️ Format salah.\n\n📌 *Cara pakai:* `manhwa nano machine`")
	}

	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/finder/shinigami?q=%s", url.QueryEscape(query))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal terhubung ke API Shinigami.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data ShinigamiSearchRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success || len(data.Result.Data) == 0 {
		_ = ctx.React("❌")
		return ctx.Reply(fmt.Sprintf("❌ Tidak ditemukan hasil untuk: %s", query))
	}

	newSession := &SessionData{
		State: "list",
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 *Hasil Pencarian Komik: %s*\n\n", strings.ToUpper(query)))

	for i, item := range data.Result.Data {
		if i >= 10 {
			break
		}
		format := "Manga"
		if len(item.Taxonomy.Format) > 0 {
			format = item.Taxonomy.Format[0].Name
		}

		sb.WriteString(fmt.Sprintf("*%d.* %s\n", i+1, item.Title))
		sb.WriteString(fmt.Sprintf("└ %s | Rating: %.1f\n\n", format, item.UserRate))
		newSession.MangaIDs = append(newSession.MangaIDs, item.MangaID)
	}

	sb.WriteString("---\nReply pesan ini dengan angka *(1-10)* untuk melihat detail komik.")

	msgID, err := ctx.ReplyWithID(sb.String())
	if err == nil {
		replyRouter.Register(msgID, "shinigami", newSession)
		_ = ctx.React("✅")
	} else {
		_ = ctx.React("❌")
	}
	return err
}

// =============================================
// 5. INTERACTION HANDLER (via reply_router)
// =============================================

func handleShinigamiReply(ctx *ContextBot, rc *ReplyContext) error {
	session, ok := rc.Data.(*SessionData)
	if !ok {
		return nil
	}

	textRaw := strings.ToLower(strings.TrimSpace(ctx.TextMessage))

	// A. NAVIGASI PREV / NEXT
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
		return sendChapterButton(ctx, chapterID, targetChapNum, session)
	}

	// B. MENGIRIM ANGKA MURNI
	index, err := strconv.Atoi(textRaw)
	if err != nil {
		return nil
	}

	if session.State == "list" {
		if index < 1 || index > len(session.MangaIDs) {
			return nil
		}
		return loadMangaDetail(ctx, session.MangaIDs[index-1])
	}

	if session.State == "detail" {
		chapterID, exists := session.ChapterIDs[textRaw]
		if !exists {
			return nil
		}
		return sendChapterButton(ctx, chapterID, textRaw, session)
	}

	return nil
}

func loadMangaDetail(ctx *ContextBot, mangaID string) error {
	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/fetcher/shinigami?mangaId=%s", url.QueryEscape(mangaID))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memuat detail komik.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data ShinigamiDetailRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success || !data.Result.Success {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Terjadi kesalahan pada sistem server.")
	}

	info := data.Result.Data.Info

	// Session Data baru (detail)
	newSession := &SessionData{
		State:      "detail",
		ChapterIDs: make(map[string]string),
		MangaTitle: info.Title,
	}

	var genres []string
	for _, g := range info.Taxonomy.Genre {
		genres = append(genres, g.Name)
	}
	format := "Manga"
	if len(info.Taxonomy.Format) > 0 {
		format = info.Taxonomy.Format[0].Name
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📄 *Detail Komik: %s*\n\n", strings.ToUpper(info.Title)))
	sb.WriteString(fmt.Sprintf("⭐ Rating : %.1f\n", info.UserRate))
	sb.WriteString(fmt.Sprintf("📌 Format : %s\n", format))
	sb.WriteString(fmt.Sprintf("🎭 Genre  : %s\n\n", strings.Join(genres, ", ")))

	synopsis := info.Description
	if len(synopsis) > 250 {
		synopsis = synopsis[:250] + "..."
	}
	sb.WriteString(fmt.Sprintf("*Sinopsis:*\n_%s_\n\n", synopsis))
	sb.WriteString("*Daftar Chapter:*\n")

	for _, ch := range data.Result.Data.Chapters {
		chNum := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", ch.ChapterNumber), "0"), ".")
		sb.WriteString(fmt.Sprintf("- Chapter %s\n", chNum))
		newSession.ChapterIDs[chNum] = ch.ChapterID
		newSession.ChNumbers = append(newSession.ChNumbers, chNum)
	}

	sb.WriteString("---\nReply pesan ini dengan angka chapter *(Contoh: 35)* untuk mulai membaca.")

	msgID, err := ctx.ReplyWithID(sb.String())
	if err == nil {
		replyRouter.Register(msgID, "shinigami", newSession)
		_ = ctx.React("✅")
	} else {
		_ = ctx.React("❌")
	}
	return err
}

func sendChapterButton(ctx *ContextBot, chapterID string, chapterNum string, base *SessionData) error {
	_ = ctx.React("⏳")
	readUrl := fmt.Sprintf("https://ps.azumi.dev/comic?shinigami=%s", chapterID)

	// State navigasi baru (bawa data chapter agar before/after & pemilihan chapter lain tetap bisa)
	navSession := &SessionData{
		State:      "detail",
		ChapterIDs: base.ChapterIDs,
		ChNumbers:  base.ChNumbers,
		MangaTitle: base.MangaTitle,
	}
	updateNavigationLinks(navSession, chapterNum)

	btn := ctx.Button()
	btn.SetTitle("📖 *MANHWA READER*")

	bodyMessage := fmt.Sprintf("🎬 *%s*\n\nChapter %s siap untuk dibaca. Pilih opsi di bawah ini.", navSession.MangaTitle, chapterNum)
	btn.SetBody(bodyMessage)
	btn.SetFooter("© Powered By Shinigami")

	btn.AddUrl("Mulai Membaca", readUrl, true)

	if navSession.PrevChapNum != "" {
		btn.AddReply("Previous Chapter", "before")
	}
	if navSession.NextChapNum != "" {
		btn.AddReply("Next Chapter", "after")
	}

	msgID, err := btn.SendToChatWithID(ctx)
	if err == nil {
		replyRouter.Register(msgID, "shinigami", navSession)
		_ = ctx.React("✅")
	} else {
		_ = ctx.React("❌")
	}
	return err
}

func updateNavigationLinks(session *SessionData, currentChapNum string) {
	session.PrevChapNum = ""
	session.NextChapNum = ""

	idx := -1
	for i, num := range session.ChNumbers {
		if num == currentChapNum {
			idx = i
			break
		}
	}

	if idx != -1 {
		// Asumsi daftar chapter dari terbaru (index kecil) ke terlama (index besar)
		if idx > 0 {
			session.NextChapNum = session.ChNumbers[idx-1]
		}
		if idx < len(session.ChNumbers)-1 {
			session.PrevChapNum = session.ChNumbers[idx+1]
		}
	}
}
