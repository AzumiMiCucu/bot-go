package src

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// AIImage = gambar input untuk provider AI yang mendukung vision (Copilot).
type AIImage struct {
	Data []byte
	Mime string
}

// =================================================================
// COPILOT (copilot.microsoft.com) — port Go dari skrip JS asli.
// =================================================================
// Alur:
//  1. POST /c/api/conversations → buat percakapan (ambil id + cookie).
//  2. WebSocket ke /c/api/chat → tunggu event "connected" → kirim {event:"send",
//     content:[{type:text,text}]} → kumpulkan "appendText" → selesai saat "done".
//
// State: cookieObj + conversationId (untuk kelanjutan percakapan).
// =================================================================

const copilotUA = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36"
const copilotBase = "https://copilot.microsoft.com"

var copilotHTTP = &http.Client{Timeout: 60 * time.Second}

type copilotState struct {
	CookieObj      map[string]string `json:"cookieObj"`
	ConversationID string            `json:"conversationId"`
}

func decodeCopilotState(s string) copilotState {
	st := copilotState{CookieObj: map[string]string{}}
	if s != "" {
		if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
			_ = json.Unmarshal(raw, &st)
		}
	}
	if st.CookieObj == nil {
		st.CookieObj = map[string]string{}
	}
	return st
}

func (st copilotState) encode() string {
	b, _ := json.Marshal(st)
	return base64.StdEncoding.EncodeToString(b)
}

func copilotHeaders(st copilotState) http.Header {
	h := http.Header{}
	h.Set("user-agent", copilotUA)
	h.Set("origin", copilotBase)
	h.Set("referer", copilotBase+"/")
	if len(st.CookieObj) > 0 {
		h.Set("cookie", serializeCookies(st.CookieObj))
	}
	return h
}

// copilotUploadAttachment mengunggah gambar ke /c/api/attachments dan mengembalikan
// URL attachment untuk disisipkan pada konten pesan (vision).
func copilotUploadAttachment(st copilotState, img *AIImage) (string, error) {
	mime := img.Mime
	if mime == "" {
		mime = "image/jpeg"
	}
	req, _ := http.NewRequest(http.MethodPost, copilotBase+"/c/api/attachments", bytes.NewReader(img.Data))
	req.Header.Set("content-type", mime)
	req.Header.Set("user-agent", copilotUA)
	req.Header.Set("referer", copilotBase+"/chats/"+st.ConversationID)
	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	if len(st.CookieObj) > 0 {
		req.Header.Set("cookie", serializeCookies(st.CookieObj))
	}
	resp, err := copilotHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload attachment: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	mergeSetCookies(st.CookieObj, resp.Header)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("upload attachment status %d", resp.StatusCode)
	}
	var a struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.URL == "" {
		return "", fmt.Errorf("attachment URL kosong")
	}
	return a.URL, nil
}

// CopilotChat mengirim `prompt` ke Copilot. `mode` ∈ {chat,smart,reasoning} (default
// chat). `img` opsional (nil = teks saja) untuk vision. `prevState` = state
// sebelumnya (base64; "" untuk percakapan baru).
func CopilotChat(prompt, mode, prevState string, img *AIImage) (string, string, error) {
	switch mode {
	case "chat", "smart", "reasoning":
	default:
		mode = "chat"
	}
	st := decodeCopilotState(prevState)

	// 1) Pastikan ada conversationId.
	if st.ConversationID == "" {
		req, _ := http.NewRequest(http.MethodPost, copilotBase+"/c/api/conversations", nil)
		req.Header = copilotHeaders(st)
		req.Header.Set("content-type", "application/json")
		req.Header.Set("accept", "application/json")
		resp, err := copilotHTTP.Do(req)
		if err != nil {
			return "", st.encode(), fmt.Errorf("buat conversation: %w", err)
		}
		mergeSetCookies(st.CookieObj, resp.Header)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			return "", st.encode(), fmt.Errorf("conversation ditolak (status %d) — kemungkinan butuh TLS spoof (tls-client)", resp.StatusCode)
		}
		var cv struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &cv)
		if cv.ID == "" {
			cv.ID = uuid.NewString()
		}
		st.ConversationID = cv.ID
	}

	// 1b) Upload gambar (vision) bila ada.
	attURL := ""
	if img != nil && len(img.Data) > 0 {
		u, err := copilotUploadAttachment(st, img)
		if err != nil {
			return "", st.encode(), err
		}
		attURL = u
	}

	// 2) WebSocket chat.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	wsURL := copilotBase + "/c/api/chat?api-version=2&features=-,ncedge,edgepagecontext&setflight=-,ncedge,edgepagecontext&ncedge=1"
	wsURL = "wss" + strings.TrimPrefix(wsURL, "https")

	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: copilotHeaders(st)})
	if err != nil {
		return "", st.encode(), fmt.Errorf("ws dial: %w — kemungkinan butuh TLS spoof", err)
	}
	c.SetReadLimit(8 << 20)
	defer c.Close(websocket.StatusNormalClosure, "done")

	ready := false
	fullText := ""
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		event, _ := msg["event"].(string)

		if !ready && event == "connected" {
			ready = true
			content := []interface{}{}
			if attURL != "" {
				content = append(content, map[string]interface{}{"type": "image", "url": attURL})
			}
			if prompt != "" {
				content = append(content, map[string]interface{}{"type": "text", "text": prompt})
			}
			send := map[string]interface{}{
				"event":          "send",
				"conversationId": st.ConversationID,
				"content":        content,
				"mode":           mode,
				"context":        map[string]interface{}{},
			}
			if err := c.Write(ctx, websocket.MessageText, mustJSON(send)); err != nil {
				return "", st.encode(), fmt.Errorf("ws send: %w", err)
			}
			continue
		}
		if !ready {
			continue
		}
		switch event {
		case "appendText":
			if t, ok := msg["text"].(string); ok {
				fullText += t
			}
		case "done":
			c.Close(websocket.StatusNormalClosure, "done")
			ft := strings.TrimSpace(fullText)
			if ft == "" {
				return "", st.encode(), fmt.Errorf("tidak ada jawaban dari Copilot")
			}
			return ft, st.encode(), nil
		case "error":
			return "", st.encode(), fmt.Errorf("copilot error: %v", msg["message"])
		}
	}

	ft := strings.TrimSpace(fullText)
	if ft == "" {
		return "", st.encode(), fmt.Errorf("koneksi Copilot terputus sebelum jawaban lengkap")
	}
	return ft, st.encode(), nil
}
