package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// =============================================
// 1. STATE DATA (di-key oleh ID pesan via reply_router)
// =============================================

type SamehadaSessionData struct {
	AnimeURLs  []string
	EpisodeMap map[int]string // Mapping Nomor Episode -> URL Episode
	PrevEpURL  string
	NextEpURL  string
	State      string    // "list" atau "detail"
	ExpiresAt  time.Time // tidak dipakai lagi (expiry ditangani reply_router)
}

// =============================================
// 2. JSON STRUCTS
// =============================================

type SamehadaSearchRes struct {
	Success bool `json:"success"`
	Result  []struct {
		Title  string `json:"title"`
		URL    string `json:"url"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Score  string `json:"score"`
	} `json:"result"`
}

type SamehadaDetailRes struct {
	Success bool `json:"success"`
	Result  struct {
		Title    string   `json:"title"`
		Poster   string   `json:"poster"`
		Rating   float64  `json:"rating"`
		Synopsis string   `json:"synopsis"`
		Genres   []string `json:"genres"`
		Details  struct {
			Status       string      `json:"status"`
			Type         string      `json:"type"`
			Studio       string      `json:"studio"`
			TotalEpisode interface{} `json:"totalEpisode"`
			Japanese     string      `json:"japanese"`
		} `json:"details"`
		Episodes []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			URL    string `json:"url"`
		} `json:"episodes"`
	} `json:"result"`
}

type SamehadaEpsRes struct {
	Success bool `json:"success"`
	Result  struct {
		Servers []struct {
			Name      string `json:"name"`
			IframeUrl string `json:"iframeUrl"`
		} `json:"servers"`
		Downloads []struct {
			Format    string `json:"format"`
			Qualities []struct {
				Quality string `json:"quality"`
				Links   []struct {
					Host string `json:"host"`
					Url  string `json:"url"`
				} `json:"links"`
			} `json:"qualities"`
		} `json:"downloads"`
	} `json:"result"`
}

// =============================================
// 3. COMMAND REGISTRATION
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "Samehadaku Search",
		Category:    "Finder",
		Aliases:     []string{"anime", "samehada"},
		Pattern:     regexp.MustCompile(`(?i)^(?:samehada|samehadaku|anime)\s+(.+)$`),
		Description: "Mencari anime dari Samehadaku",
		Execute:     ExecuteSamehadaSearch,
	})
}

// =============================================
// 4. PHASE 1: SEARCH
// =============================================

func ExecuteSamehadaSearch(ctx *ContextBot) error {
	query := strings.TrimSpace(ctx.Args)
	if query == "" {
		return ctx.Reply("⚠️ Format salah.\n\n📌 *Cara pakai:* `anime kimetsu`")
	}

	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/finder/samehada?q=%s", url.QueryEscape(query))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal terhubung ke API Samehadaku.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data SamehadaSearchRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success || len(data.Result) == 0 {
		_ = ctx.React("❌")
		return ctx.Reply(fmt.Sprintf("❌ Tidak ditemukan hasil untuk: %s", query))
	}

	newSession := &SamehadaSessionData{
		State: "list",
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 *Hasil Pencarian Anime: %s*\n\n", strings.ToUpper(query)))

	for i, anime := range data.Result {
		if i >= 10 {
			break
		}
		sb.WriteString(fmt.Sprintf("*%d.* %s\n", i+1, anime.Title))
		sb.WriteString(fmt.Sprintf("└ %s | %s | ⭐ %s\n\n", anime.Type, anime.Status, anime.Score))
		newSession.AnimeURLs = append(newSession.AnimeURLs, anime.URL)
	}

	sb.WriteString("---\nReply pesan ini dengan angka (1-10) untuk melihat detail anime.")

	msgID, err := ctx.ReplyWithID(sb.String())
	if err == nil {
		replyRouter.Register(msgID, "samehada", newSession)
		_ = ctx.React("✅")
	} else {
		_ = ctx.React("❌")
	}
	return err
}

// =============================================
// 5. PHASE 2 & 3: INTERACTION HANDLER (via reply_router)
// =============================================

func handleSamehadaReply(ctx *ContextBot, rc *ReplyContext) error {
	session, ok := rc.Data.(*SamehadaSessionData)
	if !ok {
		return nil
	}

	textRaw := strings.ToLower(strings.TrimSpace(ctx.TextMessage))

	// A. NAVIGASI PREV / NEXT
	if textRaw == "before" || textRaw == "after" {
		targetEpURL := session.PrevEpURL
		if textRaw == "after" {
			targetEpURL = session.NextEpURL
		}
		if targetEpURL == "" {
			return ctx.Reply("❌ Tautan navigasi tidak tersedia atau ini adalah episode awal/akhir.")
		}

		// Cari nomor episode target dari EpisodeMap
		targetEpNum := -1
		for num, u := range session.EpisodeMap {
			if u == targetEpURL {
				targetEpNum = num
				break
			}
		}
		return loadSamehadaEpisode(ctx, targetEpURL, targetEpNum, session.EpisodeMap)
	}

	// B. INPUT ANGKA MURNI
	inputNum, err := strconv.Atoi(textRaw)
	if err != nil {
		return nil
	}

	// State 1: Memilih Anime dari Pencarian
	if session.State == "list" {
		if inputNum < 1 || inputNum > len(session.AnimeURLs) {
			return nil
		}
		return loadSamehadaDetail(ctx, session.AnimeURLs[inputNum-1])
	}

	// State 2: Memilih Episode Anime
	if session.State == "detail" {
		epsURL, ok := session.EpisodeMap[inputNum]
		if !ok {
			return nil
		}
		return loadSamehadaEpisode(ctx, epsURL, inputNum, session.EpisodeMap)
	}

	return nil
}

func loadSamehadaDetail(ctx *ContextBot, urlStr string) error {
	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/fetcher/samehada?url=%s", url.QueryEscape(urlStr))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memuat detail.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data SamehadaDetailRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memproses data anime.")
	}

	res := data.Result

	newSession := &SamehadaSessionData{
		State:      "detail",
		EpisodeMap: make(map[int]string),
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📄 *Detail Anime: %s*\n", strings.ToUpper(res.Title)))
	if res.Details.Japanese != "" {
		sb.WriteString(fmt.Sprintf("└ _%s_\n", res.Details.Japanese))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("⭐ Rating : %.2f\n", res.Rating))
	sb.WriteString(fmt.Sprintf("📌 Status : %s\n", res.Details.Status))
	sb.WriteString(fmt.Sprintf("🎥 Studio : %s\n", res.Details.Studio))
	sb.WriteString(fmt.Sprintf("📦 Total  : %v\n", res.Details.TotalEpisode))
	sb.WriteString(fmt.Sprintf("🎭 Genre  : %s\n\n", strings.Join(res.Genres, ", ")))

	synopsisClean := strings.ReplaceAll(res.Synopsis, "\n", " ")
	sb.WriteString(fmt.Sprintf("*Sinopsis:*\n%s\n\n", synopsisClean))

	sb.WriteString("*Daftar Episode:*\n")
	for _, eps := range res.Episodes {
		sb.WriteString(fmt.Sprintf("- Episode %d\n", eps.Number))
		newSession.EpisodeMap[eps.Number] = eps.URL
	}

	sb.WriteString("---\nReply pesan ini dengan angka episode (Contoh: 1 atau 2) untuk mulai menonton.")

	msgID, err := ctx.ReplyWithID(sb.String())
	if err == nil {
		replyRouter.Register(msgID, "samehada", newSession)
		_ = ctx.React("✅")
	} else {
		_ = ctx.React("❌")
	}
	return err
}

func loadSamehadaEpisode(ctx *ContextBot, epURL string, currentEpNum int, epMap map[int]string) error {
	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/fetcher/samehada_eps?url=%s", url.QueryEscape(epURL))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memuat link episode.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data SamehadaEpsRes
	if err := json.Unmarshal(body, &data); err != nil || !data.Success {
		_ = ctx.React("❌")
		return ctx.Reply("❌ Gagal memproses data episode.")
	}

	// 1. CARI LINK STREAMING PRIORITAS (720p)
	bestStreamURL := ""
	for _, srv := range data.Result.Servers {
		if strings.Contains(strings.ToLower(srv.Name), "720p") {
			bestStreamURL = srv.IframeUrl
			break
		}
	}
	if bestStreamURL == "" && len(data.Result.Servers) > 0 {
		bestStreamURL = data.Result.Servers[0].IframeUrl
	}

	// 2. STATE NAVIGASI BARU (bawa EpisodeMap agar before/after bisa dihitung)
	navSession := &SamehadaSessionData{
		State:      "detail",
		EpisodeMap: epMap,
	}
	if prevURL, ok := epMap[currentEpNum-1]; ok {
		navSession.PrevEpURL = prevURL
	}
	if nextURL, ok := epMap[currentEpNum+1]; ok {
		navSession.NextEpURL = nextURL
	}

	// 3. MEMBUAT BUTTON MESSAGE
	btn := ctx.Button()
	btn.SetTitle("📺 *STREAMING ENGINE*")

	bodyMessage := fmt.Sprintf("🎬 *Episode %d*\n\nPilih opsi dibawah untuk memulai.", currentEpNum)
	btn.SetBody(bodyMessage)
	btn.SetFooter("© Powered By Samehadaku")

	if bestStreamURL != "" {
		btn.AddUrl("Tonton Sekarang", bestStreamURL, true)
	}
	if navSession.PrevEpURL != "" {
		btn.AddReply("Previous Episode", "before")
	}
	if navSession.NextEpURL != "" {
		btn.AddReply("Next Episode", "after")
	}

	msgID, err := btn.SendToChatWithID(ctx)
	if err == nil {
		replyRouter.Register(msgID, "samehada", navSession)
		_ = ctx.React("✅")
	} else {
		_ = ctx.React("❌")
	}
	return err
}
