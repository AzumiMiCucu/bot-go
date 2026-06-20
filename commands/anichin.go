package commands // Sesuaikan dengan nama package kamu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src" // Sesuaikan dengan nama modul di go.mod kamu

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// =============================================
// 1. STATE DATA (di-key oleh ID pesan via reply_router)
// =============================================

// DonghuaSessionData menyimpan state khusus untuk interaksi Anichin/Donghua
type DonghuaSessionData struct {
	DonghuaURLs []string
	EpisodeURLs map[string]string
	PrevEpURL   string
	NextEpURL   string
	State       string    // "list" atau "detail"
	ExpiresAt   time.Time // tidak dipakai lagi (expiry ditangani reply_router)
}

// =============================================
// 2. JSON STRUCT DEFINITIONS
// =============================================

type SearchAPIResponse struct {
	Success bool `json:"success"`
	Result  []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		URL      string `json:"url"`
		ImageUrl string `json:"imageUrl"`
		Type     string `json:"type"`
		Status   string `json:"status"`
		Subtitle string `json:"subtitle"`
	} `json:"result"`
}
type DetailAPIResponse struct {
	Success bool `json:"success"`
	Result  struct {
		Title            string   `json:"title"`
		AlternativeTitle string   `json:"alternativeTitle"`
		Rating           string   `json:"rating"`
		Synopsis         string   `json:"synopsis"`
		Genres           []string `json:"genres"`
		Metadata         struct {
			Status   string `json:"status"`
			Studio   string `json:"studio"`
			Episodes string `json:"episodes"`
			Type     string `json:"type"`
		} `json:"metadata"`
		EpisodeList []struct {
			EpisodeOriginal string `json:"episodeNumber"`
			URL             string `json:"url"`
		} `json:"episodeList"`
	} `json:"result"`
}

type EpisodeAPIResponse struct {
	Success bool `json:"success"`
	Result  struct {
		EpisodeTitle  string `json:"episodeTitle"`
		EpisodeNumber string `json:"episodeNumber"`
		SeriesUrl     string `json:"seriesUrl"`
		Navigation    struct {
			PrevEpisode *string `json:"prevEpisode"`
			NextEpisode *string `json:"nextEpisode"`
		} `json:"navigation"`
		VideoServers []struct {
			Server   string `json:"server"`
			VideoURL string `json:"videoUrl"`
		} `json:"videoServers"`
	} `json:"result"`
}

// =============================================
// 3. COMMAND REGISTRATIONS
// =============================================

func init() {
	RegisterCommand(Command{
		Name:        "Donghua Search",
		Category:    "Finder",
		Aliases:     []string{"donghua", "anichin"},
		Pattern:     regexp.MustCompile(`(?i)^(?:donghua|anichin)\s+(.+)`),
		Description: "Mencari donghua dari Anichin API",
		Execute:     ExecuteDonghuaSearch,
	})
}

// =============================================
// 4. PHASE 1: EXECUTE Donghua SEARCH
// =============================================
func ExecuteDonghuaSearch(ctx *src.ContextBot) error {
	client := ctx.Client
	chatJID := ctx.Msg.Info.Chat
	query := strings.TrimSpace(ctx.Args)

	if query == "" {
		_, err := client.SendMessage(context.Background(), chatJID, &waE2E.Message{
			Conversation: proto.String("❌ Format salah. Contoh: *anichin tales herding gods*"),
		})
		return err
	}

	ctx.Reply("⏳ Memproses pencarian donghua...")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/finder/anichin?q=%s", url.QueryEscape(query))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		ctx.Reply("❌ Gagal terhubung ke API.")
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data SearchAPIResponse
	if err := json.Unmarshal(body, &data); err != nil || !data.Success || len(data.Result) == 0 {
		ctx.Reply(fmt.Sprintf("❌ Tidak ditemukan hasil untuk: %s", query))
		return err
	}

	// Buat Session Baru
	newSession := &DonghuaSessionData{
		State: "list",
	}

	richBuilder := src.NewAIRich().
		SetTitle(fmt.Sprintf("Hasil Pencarian: %s", strings.ToUpper(query)))

	for i, donghua := range data.Result {
		if i >= 10 {
			break
		}

		// Simpan URL ke session
		newSession.DonghuaURLs = append(newSession.DonghuaURLs, donghua.URL)

		teksInfo := fmt.Sprintf("*%d.* \n", i+1)
		richBuilder.AddText(teksInfo)

		richBuilder.AddProduct(src.AIProduct{
			Title:      donghua.Title,
			Brand:      donghua.Type,
			Price:      donghua.Status,
			SalePrice:  donghua.Subtitle,
			ProductURL: donghua.URL,
			ImageURL:   donghua.ImageUrl,
		})
	}

	// Kirim Rich UI & daftarkan ID pesan ke reply-router
	msgID, err := richBuilder.SendToChatWithID(ctx)
	if err != nil {
		fmt.Println("❌ Gagal mengirim Donghua Rich Message:", err)
		return err
	}
	replyRouter.Register(msgID, "donghua", newSession)
	return nil
}

// =============================================
// 5. PHASE 2 & 3: INTERACTIVE HANDLER (via reply_router)
// =============================================

func handleDonghuaReply(ctx *ContextBot, rc *ReplyContext) error {
	session, ok := rc.Data.(*DonghuaSessionData)
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
			return ctx.Reply("❌ Tautan navigasi tidak tersedia atau kedaluwarsa.")
		}
		return loadEpisodeDetail(ctx, targetEpURL, session)
	}

	// B. MENGIRIM ANGKA MURNI
	index, err := strconv.Atoi(textRaw)
	if err != nil {
		return nil
	}

	// Sesi 1: Memilih Donghua dari List Hasil Pencarian
	if session.State == "list" {
		if index < 1 || index > len(session.DonghuaURLs) {
			return nil
		}
		return loadDonghuaDetail(ctx, session.DonghuaURLs[index-1])
	}

	// Sesi 2: Memilih Nomor Episode dari Detail Donghua
	if session.State == "detail" {
		targetURL, exists := session.EpisodeURLs[textRaw]
		if !exists {
			return nil
		}
		return loadEpisodeDetail(ctx, targetURL, session)
	}

	return nil
}

func loadDonghuaDetail(ctx *ContextBot, donghuaURL string) error {
	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/fetcher/anichin?url=%s", url.QueryEscape(donghuaURL))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		return ctx.Reply("❌ Gagal memuat detail.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data DetailAPIResponse
	if err := json.Unmarshal(body, &data); err != nil || !data.Success {
		return ctx.Reply("❌ Terjadi kesalahan pada sistem server.")
	}

	// Session Data baru (detail)
	newSession := &DonghuaSessionData{
		State:       "detail",
		EpisodeURLs: make(map[string]string),
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📄 *Detail Donghua: %s*\n", strings.ToUpper(data.Result.Title)))
	if data.Result.AlternativeTitle != "" {
		sb.WriteString(fmt.Sprintf("└ _%s_\n", data.Result.AlternativeTitle))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("⭐ Rating : %s\n", data.Result.Rating))
	sb.WriteString(fmt.Sprintf("📌 Status : %s\n", data.Result.Metadata.Status))
	sb.WriteString(fmt.Sprintf("🎥 Studio : %s\n", data.Result.Metadata.Studio))
	sb.WriteString(fmt.Sprintf("📦 Total  : %s\n", data.Result.Metadata.Episodes))
	sb.WriteString(fmt.Sprintf("🎭 Genre  : %s\n\n", strings.Join(data.Result.Genres, ", ")))
	sb.WriteString(fmt.Sprintf("*Sinopsis:*\n%s\n\n", data.Result.Synopsis))

	sb.WriteString("*Daftar Episode:*\n")

	for _, ep := range data.Result.EpisodeList {
		re := regexp.MustCompile(`\d+`)
		epNum := re.FindString(ep.EpisodeOriginal)
		if epNum == "" {
			epNum = ep.EpisodeOriginal
		}
		sb.WriteString(fmt.Sprintf("- Episode %s\n", epNum))
		newSession.EpisodeURLs[epNum] = ep.URL
	}

	sb.WriteString("---\nReply pesan ini dengan angka episode (Contoh: 1 atau 01) untuk mulai menonton.")

	msgID, err := ctx.ReplyWithID(sb.String())
	if err == nil {
		replyRouter.Register(msgID, "donghua", newSession)
	}
	return err
}

func loadEpisodeDetail(ctx *ContextBot, epURL string, base *DonghuaSessionData) error {
	_ = ctx.React("⏳")

	apiUrl := fmt.Sprintf("https://ps.azumi.dev/d/fetcher/anichin_eps?url=%s", url.QueryEscape(epURL))
	resp, err := httpClient.Get(apiUrl)
	if err != nil {
		return ctx.Reply("❌ Gagal mengekstrak episode.")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data EpisodeAPIResponse
	if err := json.Unmarshal(body, &data); err != nil || !data.Success {
		return ctx.Reply("❌ Terjadi kesalahan server.")
	}

	premiumStreamURL := ""
	for _, srv := range data.Result.VideoServers {
		if strings.ToLower(srv.Server) == "premium" {
			premiumStreamURL = srv.VideoURL
			break
		}
	}
	if premiumStreamURL == "" && len(data.Result.VideoServers) > 0 {
		premiumStreamURL = data.Result.VideoServers[0].VideoURL
	}

	// State navigasi baru (bawa EpisodeURLs agar pemilihan episode lain tetap bisa)
	navSession := &DonghuaSessionData{
		State:       "detail",
		EpisodeURLs: base.EpisodeURLs,
	}

	btn := ctx.Button()
	btn.SetTitle("📺 *STREAMING ENGINE*")

	bodyMessage := fmt.Sprintf("🎬 *%s*\n\nPilih opsi di bawah ini untuk memulai.", data.Result.EpisodeTitle)
	btn.SetBody(bodyMessage)
	btn.SetFooter("© Powered By Anichin")

	if premiumStreamURL != "" {
		btn.AddUrl("Tonton Sekarang", premiumStreamURL, true)
	}

	if data.Result.Navigation.PrevEpisode != nil {
		navSession.PrevEpURL = *data.Result.Navigation.PrevEpisode
		btn.AddReply("Previous Episode", "before")
	}
	if data.Result.Navigation.NextEpisode != nil {
		navSession.NextEpURL = *data.Result.Navigation.NextEpisode
		btn.AddReply("Next Episode", "after")
	}

	msgID, err := btn.SendToChatWithID(ctx)
	if err == nil {
		replyRouter.Register(msgID, "donghua", navSession)
	}
	return err
}
