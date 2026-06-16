package commands

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// --- DEKRIPSI KUNCI RAHASIA INFLACT ---
var rArr = []int{57, 100, 48, 54, 51, 60, 48, 102}
var aArr = []int{51, 50, 48, 101, 102, 53, 48, 63}
var cArr = []int{98, 53, 59, 55, 51, 100, 103, 100}
var uArr = []int{49, 103, 52, 50, 49, 100, 51, 100}
var mArr = []int{99, 48, 96, 98, 98, 96, 101, 62}
var hArr = []int{53, 49, 53, 54, 97, 50, 99, 62}
var _Arr = []int{55, 57, 97, 55, 50, 61, 101, 62}
var vArr = []int{100, 101, 97, 55, 103, 51, 54, 97}

func decodeInflactKeyPart(arr []int) string {
	var sb strings.Builder
	length := len(arr)
	for i, val := range arr {
		char := rune(val ^ (i % length))
		sb.WriteRune(char)
	}
	return sb.String()
}

func getMasterKey() string {
	return decodeInflactKeyPart(rArr) + decodeInflactKeyPart(cArr) +
		decodeInflactKeyPart(aArr) + decodeInflactKeyPart(uArr) +
		decodeInflactKeyPart(mArr) + decodeInflactKeyPart(hArr) +
		decodeInflactKeyPart(_Arr) + decodeInflactKeyPart(vArr)
}

func generateRandomHex(n int) string {
	bytes := make([]byte, n)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// Helper Format Angka IG
func formatIGNumber(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000.0)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}

// --- STRUKTUR JSON PENGAMBILAN POST & REELS ---
type IGEdgeNode struct {
	Typename           string `json:"__typename"`
	Shortcode          string `json:"shortcode"`
	VideoViewCount     int    `json:"video_view_count"`
	EdgeLikedBy        struct{ Count int `json:"count"` } `json:"edge_liked_by"`
	EdgeMediaToComment struct{ Count int `json:"count"` } `json:"edge_media_to_comment"`
	EdgeMediaToCaption struct {
		Edges []struct {
			Node struct{ Text string `json:"text"` } `json:"node"`
		} `json:"edges"`
	} `json:"edge_media_to_caption"`
}

type InflactResponse struct {
	Status string `json:"status"`
	Data   struct {
		Profile struct {
			Username   string `json:"username"`
			FullName   string `json:"full_name"`
			Biography  string `json:"biography"`
			IsPrivate  bool   `json:"is_private"`
			IsVerified bool   `json:"is_verified"`
			ProfilePic string `json:"profile_pic_url_hd"`
			Followers  struct{ Count int `json:"count"` } `json:"edge_followed_by"`
			Following  struct{ Count int `json:"count"` } `json:"edge_follow"`
			Posts      struct {
				Count int `json:"count"`
				Edges []struct {
					Node IGEdgeNode `json:"node"`
				} `json:"edges"`
			} `json:"edge_owner_to_timeline_media"`
			Reels struct {
				Count int `json:"count"`
				Edges []struct {
					Node IGEdgeNode `json:"node"`
				} `json:"edges"`
			} `json:"edge_felix_video_timeline"`
		} `json:"profile"`
	} `json:"data"`
}

func init() {
	RegisterCommand(Command{
		Name:        "Instagram Stalker",
		Category:    "Finder",
		Aliases:     []string{"igstalk", "stalkig"},
		Pattern:     regexp.MustCompile(`(?i)^(?:igstalk|stalkig|igprofile)\s+(.+)`),
		Description: "Mengambil data profil Instagram dan Postingan Terbaru",
		Price:       0.010,
		Execute:     ExecuteIGStalk,
	})
}

func ExecuteIGStalk(ctx *ContextBot) error {
	targetUsername := strings.TrimSpace(ctx.Args)
	targetUsername = strings.TrimPrefix(targetUsername, "@")

	if targetUsername == "" {
		return ctx.Reply("⚠️ Harap masukkan username Instagram yang ingin dicari.\nContoh: `igstalk jokowi`")
	}

	_ = ctx.Reply(fmt.Sprintf("🔍 Mengekstrak data profil IG *@%s*", targetUsername))

	userAgent := "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36"
	client := &http.Client{Timeout: 25 * time.Second}

	// 1. TAHAP PERTAMA: Mengambil Session Cookies dan CSRF Token
	reqInit, _ := http.NewRequest("GET", "https://inflact.com/instagram-viewer/profile/", nil)
	reqInit.Header.Set("User-Agent", userAgent)
	reqInit.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9")
	reqInit.Header.Set("Accept-Language", "en-ID,en;q=0.9")

	respInit, err := client.Do(reqInit)
	if err != nil {
		return ctx.Reply("❌ Gagal terhubung ke server utama Inflact.")
	}
	defer respInit.Body.Close()

	var cookieStrs []string
	for _, cookie := range respInit.Cookies() {
		cookieStrs = append(cookieStrs, cookie.Name+"="+cookie.Value)
	}
	freshCookies := strings.Join(cookieStrs, "; ") + ";"

	bodyInitBytes, _ := io.ReadAll(respInit.Body)
	bodyInitString := string(bodyInitBytes)

	csrfRegex := regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)">`)
	csrfMatch := csrfRegex.FindStringSubmatch(bodyInitString)
	csrfToken := ""
	if len(csrfMatch) > 1 {
		csrfToken = csrfMatch[1]
	}

	// 2. TAHAP KEDUA: Meracik Kriptografi (HMAC-SHA256, Base64, Nonce)
	currentTimestamp := time.Now().Unix()
	fakeClientId := generateRandomHex(16)
	fakeNonce := generateRandomHex(16)

	payloadString := fmt.Sprintf(`{"timestamp":%d,"clientId":"%s","nonce":"%s"}`, currentTimestamp, fakeClientId, fakeNonce)
	clientToken := base64.StdEncoding.EncodeToString([]byte(payloadString))

	mac := hmac.New(sha256.New, []byte(getMasterKey()))
	mac.Write([]byte(payloadString))
	clientSignature := hex.EncodeToString(mac.Sum(nil))

	// 3. TAHAP KETIGA: POST ke API Inflact
	formData := url.Values{}
	formData.Set("url", targetUsername)

	reqApi, _ := http.NewRequest("POST", "https://inflact.com/downloader/api/viewer/profile/?lang=en", strings.NewReader(formData.Encode()))
	reqApi.Header.Set("User-Agent", userAgent)
	reqApi.Header.Set("Accept", "application/json, text/plain, */*")
	reqApi.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqApi.Header.Set("origin", "https://inflact.com")
	reqApi.Header.Set("referer", "https://inflact.com/instagram-viewer/profile/")
	reqApi.Header.Set("X-CSRF-Token", csrfToken)
	reqApi.Header.Set("X-Requested-With", "XMLHttpRequest")
	reqApi.Header.Set("x-client-token", clientToken)
	reqApi.Header.Set("x-client-signature", clientSignature)
	reqApi.Header.Set("Cookie", freshCookies)

	respApi, err := client.Do(reqApi)
	if err != nil {
		return ctx.Reply("❌ Terjadi kesalahan saat memproses data Instagram.")
	}
	defer respApi.Body.Close()

	apiBodyBytes, _ := io.ReadAll(respApi.Body)

	var resultJson InflactResponse
	json.Unmarshal(apiBodyBytes, &resultJson)

	if resultJson.Status != "success" {
		return ctx.Reply(fmt.Sprintf("❌ Gagal menemukan profil *@%s*. Pastikan username benar dan akun tidak ditangguhkan.", targetUsername))
	}

	profile := resultJson.Data.Profile

	// 4. TAHAP KEEMPAT: UI Formatting Dasar
	privStatus := "Buka (Public)"
	if profile.IsPrivate {
		privStatus = "Terkunci (Private)"
	}

	verifStatus := "Bukan"
	if profile.IsVerified {
		verifStatus = "Verified ✅"
	}

	bio := profile.Biography
	if bio == "" {
		bio = "_Tidak ada biografi_"
	}

	var sb strings.Builder
	sb.WriteString("✅ *INSTAGRAM PROFILE*\n\n")
	sb.WriteString(fmt.Sprintf("👤 *Name:* %s\n", profile.FullName))
	sb.WriteString(fmt.Sprintf("🏷️ *Username:* @%s\n", profile.Username))
	sb.WriteString(fmt.Sprintf("🔒 *Status:* %s\n", privStatus))
	sb.WriteString(fmt.Sprintf("✔️ *Verified:* %s\n\n", verifStatus))

	sb.WriteString("📊 *Statistik:*\n")
	sb.WriteString(fmt.Sprintf("├ *Followers:* %s\n", formatIGNumber(profile.Followers.Count)))
	sb.WriteString(fmt.Sprintf("├ *Following:* %s\n", formatIGNumber(profile.Following.Count)))
	sb.WriteString(fmt.Sprintf("└ *Posts:* %s\n\n", formatIGNumber(profile.Posts.Count)))

	sb.WriteString(fmt.Sprintf("📝 *Bio:*\n%s\n\n", bio))
	sb.WriteString(fmt.Sprintf("🔗 *Link:* https://instagram.com/%s\n", profile.Username))

	// TAHAP KELIMA: Ekstraksi Postingan/Reels Terbaru (Bila Tidak Private)
	if !profile.IsPrivate {
		// Menggabungkan semua post dan reels
		var allMedia []IGEdgeNode
		for _, edge := range profile.Posts.Edges {
			allMedia = append(allMedia, edge.Node)
		}
		for _, edge := range profile.Reels.Edges {
			allMedia = append(allMedia, edge.Node)
		}

		if len(allMedia) > 0 {
			limit := 5
			if len(allMedia) < limit {
				limit = len(allMedia)
			}

			sb.WriteString(fmt.Sprintf("\n🎬 *%d Postingan/Reels Terbaru:*\n", limit))
			for i, media := range allMedia[:limit] {
				
				caption := "Tanpa Caption"
				if len(media.EdgeMediaToCaption.Edges) > 0 {
					caption = media.EdgeMediaToCaption.Edges[0].Node.Text
					// Potong caption agar tidak memenuhi layar
					if len(caption) > 60 {
						caption = strings.ReplaceAll(caption, "\n", " ")
						caption = caption[:57] + "..."
					}
				}

				sb.WriteString(fmt.Sprintf("\n*%d. %s*\n", i+1, caption))
				
				// Instagram hanya memberi tahu views jika tipenya adalah Video
				if media.Typename == "GraphVideo" {
					sb.WriteString(fmt.Sprintf("👁️ %s Views | ❤️ %s Likes | 💬 %s Komen\n", 
						formatIGNumber(media.VideoViewCount), 
						formatIGNumber(media.EdgeLikedBy.Count),
						formatIGNumber(media.EdgeMediaToComment.Count),
					))
				} else {
					sb.WriteString(fmt.Sprintf("❤️ %s Likes | 💬 %s Komen\n", 
						formatIGNumber(media.EdgeLikedBy.Count),
						formatIGNumber(media.EdgeMediaToComment.Count),
					))
				}
				sb.WriteString(fmt.Sprintf("🔗 https://instagram.com/p/%s\n", media.Shortcode))
			}
		}
	}

	replyText := sb.String()

	// 6. TAHAP KEENAM: Kirim Gambar
	var finalMsg *waProto.Message
	if profile.ProfilePic != "" {
		imgResp, err := http.Get(profile.ProfilePic)
		if err == nil {
			defer imgResp.Body.Close()
			imgBytes, _ := io.ReadAll(imgResp.Body)
			
			uploaded, errUpload := ctx.Client.Upload(context.Background(), imgBytes, whatsmeow.MediaImage)
			if errUpload == nil {
				finalMsg = &waProto.Message{
					ImageMessage: &waProto.ImageMessage{
						Caption:       proto.String(replyText),
						URL:           proto.String(uploaded.URL),
						DirectPath:    proto.String(uploaded.DirectPath),
						MediaKey:      uploaded.MediaKey,
						Mimetype:      proto.String("image/jpeg"),
						FileEncSHA256: uploaded.FileEncSHA256,
						FileSHA256:    uploaded.FileSHA256,
						FileLength:    proto.Uint64(uint64(len(imgBytes))),
					},
				}
			}
		}
	}

	if finalMsg != nil {
		senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
		if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
			senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
		}
		finalMsg.ImageMessage.ContextInfo = &waProto.ContextInfo{
			StanzaID:      proto.String(ctx.Msg.Info.ID),
			Participant:   proto.String(senderStr),
			QuotedMessage: ctx.Msg.Message,
		}
		_, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID.ToNonAD(), finalMsg)
		if err == nil {
			return nil
		}
	}

	return ctx.Reply(replyText)
}