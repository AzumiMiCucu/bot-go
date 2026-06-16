package commands

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "TikTok Stalk",
		Category:    "Finder",
		Aliases:     []string{"ttstalk", "stalktt"},
		Pattern:     regexp.MustCompile(`(?i)^(?:ttstalk|stalktt|tiktokstalk)\s+(.+)`),
		Description: "Stalking profil dan video terbaru akun TikTok",
		Execute:     ExecuteTikTokStalk,
	})
}

// =============================================
// STRUKTUR DATA JSON
// =============================================

type TTProfilePayload struct {
	UniqueID string `json:"unique_id"`
}

type TTVideoPayload struct {
	SecUid   string `json:"secUid"`
	UniqueID string `json:"unique_id"`
	Count    int    `json:"count"`
	Cursor   int    `json:"cursor"`
}

type TTProfileResponse struct {
	Code int `json:"code"`
	Data struct {
		User struct {
			UniqueID       string `json:"uniqueId"`
			Nickname       string `json:"nickname"`
			Signature      string `json:"signature"`
			SecUid         string `json:"secUid"`
			PrivateAccount bool   `json:"privateAccount"`
			Verified       bool   `json:"verified"`
			AvatarLarger   string `json:"avatarLarger"`
			AvatarMedium   string `json:"avatarMedium"`
		} `json:"user"`
		Stats struct {
			FollowerCount  int `json:"followerCount"`
			FollowingCount int `json:"followingCount"`
			HeartCount     int `json:"heartCount"`
			VideoCount     int `json:"videoCount"`
		} `json:"stats"`
	} `json:"data"`
}

type TTVideoResponse struct {
	Code int `json:"code"`
	Data struct {
		Videos []struct {
			VideoID      string `json:"video_id"`
			Title        string `json:"title"`
			PlayCount    int    `json:"play_count"`
			DiggCount    int    `json:"digg_count"`
			CommentCount int    `json:"comment_count"`
			ShareCount   int    `json:"share_count"`
		} `json:"videos"`
	} `json:"data"`
}

// =============================================
// EXECUTE
// =============================================

func ExecuteTikTokStalk(ctx *ContextBot) error {
	username := strings.TrimSpace(ctx.Args)
	if username == "" {
		matches := regexp.MustCompile(`(?i)^(?:ttstalk|stalktt|tiktokstalk)\s+(.+)`).FindStringSubmatch(ctx.TextMessage)
		if len(matches) > 1 {
			username = strings.TrimSpace(matches[1])
		}
	}

	username = strings.TrimPrefix(username, "@")

	if username == "" {
		return ctx.Reply("❌ Masukkan username TikTok.\n\nContoh: *ttstalk smanda.cerita*")
	}

	_ = ctx.Reply(fmt.Sprintf("🔍 Mengekstrak data profil TikTok *@%s*, mohon tunggu...", username))

	// 1. Ambil Profil TikTok
	profileReqBody, _ := json.Marshal(TTProfilePayload{UniqueID: username})
	profileRaw, err := doTTRequest("https://ttviewer.net/api/tiktok/get-profile", profileReqBody)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Gagal menghubungi API profil: %v", err))
	}

	var profileResp TTProfileResponse
	if err := json.Unmarshal(profileRaw, &profileResp); err != nil || profileResp.Code != 0 || profileResp.Data.User.UniqueID == "" {
		return ctx.Reply(fmt.Sprintf("😕 Profil *@%s* tidak ditemukan atau API sedang bermasalah.", username))
	}

	user := profileResp.Data.User
	stats := profileResp.Data.Stats

	// Format Status & Verifikasi
	privStatus := "Buka (Public)"
	if user.PrivateAccount {
		privStatus = "Terkunci (Private)"
	}

	verifStatus := "Bukan"
	if user.Verified {
		verifStatus = "Verified ✅"
	}

	bio := user.Signature
	if bio == "" {
		bio = "_Tidak ada biografi_"
	}

	// Merakit Teks UI Mirip IG Stalk
	var sb strings.Builder
	sb.WriteString("✅ *TIKTOK PROFILE*\n\n")
	sb.WriteString(fmt.Sprintf("👤 *Name:* %s\n", user.Nickname))
	sb.WriteString(fmt.Sprintf("🏷️ *Username:* @%s\n", user.UniqueID))
	sb.WriteString(fmt.Sprintf("🔒 *Status:* %s\n", privStatus))
	sb.WriteString(fmt.Sprintf("✔️ *Verified:* %s\n\n", verifStatus))

	sb.WriteString("📊 *Statistik:*\n")
	sb.WriteString(fmt.Sprintf("├ *Followers:* %s\n", formatNumber(stats.FollowerCount)))
	sb.WriteString(fmt.Sprintf("├ *Following:* %s\n", formatNumber(stats.FollowingCount)))
	sb.WriteString(fmt.Sprintf("├ *Likes:* %s\n", formatNumber(stats.HeartCount)))
	sb.WriteString(fmt.Sprintf("└ *Videos:* %s\n\n", formatNumber(stats.VideoCount)))

	sb.WriteString(fmt.Sprintf("📝 *Bio:*\n%s\n\n", bio))
	sb.WriteString(fmt.Sprintf("🔗 *Link:* https://tiktok.com/@%s\n", user.UniqueID))

	// 2. Ambil Video Terbaru (Jika Tidak Private)
	if !user.PrivateAccount {
		videoReqBody, _ := json.Marshal(TTVideoPayload{
			SecUid:   user.SecUid,
			UniqueID: username,
			Count:    9,
			Cursor:   0,
		})

		videoRaw, err := doTTRequest("https://ttviewer.net/api/tiktok/get-videos", videoReqBody)
		if err == nil {
			var videoResp TTVideoResponse
			if err := json.Unmarshal(videoRaw, &videoResp); err == nil && videoResp.Code == 0 && len(videoResp.Data.Videos) > 0 {
				limit := 5
				if len(videoResp.Data.Videos) < limit {
					limit = len(videoResp.Data.Videos)
				}

				sb.WriteString(fmt.Sprintf("\n🎬 *%d Video Terbaru:*\n", limit))
				for i, v := range videoResp.Data.Videos[:limit] {
					title := v.Title
					if len(title) > 60 {
						title = title[:57] + "..."
					} else if title == "" {
						title = "Tanpa Judul"
					}

					sb.WriteString(fmt.Sprintf("\n*%d. %s*\n", i+1, title))
					sb.WriteString(fmt.Sprintf("👁️ %s Views | ❤️ %s Likes\n", formatNumber(v.PlayCount), formatNumber(v.DiggCount)))
					sb.WriteString(fmt.Sprintf("🔗 https://www.tiktok.com/@%s/video/%s\n", user.UniqueID, v.VideoID))
				}
			}
		}
	}

	replyText := sb.String()

	// 3. Download Profile Picture & Kirim via WhatsApp
	var finalMsg *waProto.Message
	targetPP := user.AvatarLarger
	if targetPP == "" {
		targetPP = user.AvatarMedium
	}

	if targetPP != "" {
		imgResp, err := http.Get(targetPP)
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

// =============================================
// HTTP REQUEST HANDLER (Aman dari Brotli/Zstd)
// =============================================

var ttClient = &http.Client{
	Timeout: 20 * time.Second,
}

func doTTRequest(url string, payload []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Encoding", "gzip, deflate") 
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("origin", "https://ttviewer.net")
	req.Header.Set("referer", "https://ttviewer.net/")
	req.Header.Set("sec-ch-ua-platform", `"Android"`)

	resp, err := ttClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var bodyReader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gagal decompress gzip: %w", err)
		}
		defer gz.Close()
		bodyReader = gz
	}

	return io.ReadAll(bodyReader)
}

// Mengubah 1200000 menjadi 1.2M, 3500 menjadi 3.5K, dsb
func formatNumber(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000.0)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}