package src

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"
	"golang.org/x/crypto/sha3"
)

// =================================================================
// CHATGPT (chatgpt.com/backend-anon) — port Go dari skrip JS asli.
// =================================================================
// chatgpt.com diproteksi Cloudflare yang memblokir TLS fingerprint Go standar
// (terbukti 403). Karena itu provider ini memakai `tls-client` (meniru TLS+HTTP
// fingerprint Chrome) — bukan net/http biasa seperti provider lain.
//
// Alur:
//  1. POST /sentinel/chat-requirements (kirim init proof) → {token, proofofwork}.
//  2. Pecahkan proof-of-work (sha3-512) → token bukti.
//  3. POST /conversation (SSE) dgn kedua token → baca stream, ambil teks asisten.
//
// State: deviceId (tetap), convId, parentMessageId (untuk kelanjutan percakapan).
// =================================================================

const chatgptUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
const chatgptBase = "https://chatgpt.com/backend-anon"

var (
	chatgptClient     tls_client.HttpClient
	chatgptClientOnce sync.Once
	chatgptClientErr  error
)

// getChatgptClient membangun (sekali) klien tls-client dengan fingerprint Chrome.
func getChatgptClient() (tls_client.HttpClient, error) {
	chatgptClientOnce.Do(func() {
		opts := []tls_client.HttpClientOption{
			tls_client.WithTimeoutSeconds(120),
			tls_client.WithClientProfile(profiles.Chrome_131),
			tls_client.WithCookieJar(tls_client.NewCookieJar()),
		}
		chatgptClient, chatgptClientErr = tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	})
	return chatgptClient, chatgptClientErr
}

type chatgptState struct {
	DeviceID        string `json:"deviceId"`
	ConvID          string `json:"convId"`
	ParentMessageID string `json:"parentMessageId"`
}

func decodeChatgptState(s string) chatgptState {
	var st chatgptState
	if s != "" {
		if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
			_ = json.Unmarshal(raw, &st)
		}
	}
	if st.DeviceID == "" {
		st.DeviceID = uuid.NewString()
	}
	if st.ParentMessageID == "" {
		st.ParentMessageID = uuid.NewString()
	}
	return st
}

func (st chatgptState) encode() string {
	b, _ := json.Marshal(st)
	return base64.StdEncoding.EncodeToString(b)
}

// generateBaseCfg meniru konfigurasi browser yang di-hash pada proof-of-work.
// Isi sebenarnya tak divalidasi server (hanya hash seed+b64 yang dicek).
func generateBaseCfg() []interface{} {
	cores := []int{8, 12, 16, 24}
	screens := []int{3000, 4000, 6000}
	return []interface{}{
		cores[rand.Intn(len(cores))] + screens[rand.Intn(len(screens))],
		time.Now().Format("Mon Jan 02 2006 15:04:05 GMT-0700 (MST)"),
		int64(4294705152),
		0,
		chatgptUA,
		"https://cdn.oaistatic.com/assets/main.js",
		"dpl=abc",
		"en-US",
		"en-US,en",
		0,
		"webdriver-false",
		"location",
		uuid.NewString(),
	}
}

// solvePow mencari b64 sehingga sha3-512(seed+b64) berawalan <= difficulty.
func solvePow(seed, diff string) string {
	cfg := generateBaseCfg()
	for i := 0; i < 500000; i++ {
		cfg[3] = i
		b, _ := json.Marshal(cfg)
		b64 := base64.StdEncoding.EncodeToString(b)
		sum := sha3.Sum512([]byte(seed + b64))
		h := hex.EncodeToString(sum[:])
		if len(h) >= len(diff) && h[:len(diff)] <= diff {
			return "gAAAAAB" + b64
		}
	}
	return "wQ8jGT" + base64.StdEncoding.EncodeToString([]byte(seed))
}

type chatgptReqData struct {
	Token       string `json:"token"`
	ProofOfWork struct {
		Seed       string `json:"seed"`
		Difficulty string `json:"difficulty"`
	} `json:"proofofwork"`
}

type cgptMessage struct {
	ID     string `json:"id"`
	Author struct {
		Role string `json:"role"`
	} `json:"author"`
	Content struct {
		ContentType string            `json:"content_type"`
		Parts       []json.RawMessage `json:"parts"`
	} `json:"content"`
}

type cgptEvent struct {
	Message        *cgptMessage `json:"message"`
	ConversationID string       `json:"conversation_id"`
}

// ChatGPTChat mengirim `prompt` ke ChatGPT anon. `prevState` = state sebelumnya
// (base64; "" untuk percakapan baru). Mengembalikan teks, state baru, error.
func ChatGPTChat(prompt, prevState string) (string, string, error) {
	st := decodeChatgptState(prevState)
	client, err := getChatgptClient()
	if err != nil {
		return "", st.encode(), fmt.Errorf("init tls-client: %w", err)
	}

	// 1) Requirements + proof seed.
	initP := "gAAAAAC" + base64.StdEncoding.EncodeToString(mustJSON(generateBaseCfg()))
	rq, _ := http.NewRequest(http.MethodPost, chatgptBase+"/sentinel/chat-requirements",
		bytes.NewReader(mustJSON(map[string]interface{}{"p": initP})))
	chatgptHeaders(rq, st.DeviceID, "application/json", "*/*")
	rr, err := client.Do(rq)
	if err != nil {
		return "", st.encode(), fmt.Errorf("chat-requirements: %w", err)
	}
	if rr.StatusCode == 403 || rr.StatusCode == 429 {
		rr.Body.Close()
		return "", st.encode(), fmt.Errorf("chat-requirements ditolak (status %d)", rr.StatusCode)
	}
	var reqData chatgptReqData
	derr := json.NewDecoder(rr.Body).Decode(&reqData)
	rr.Body.Close()
	if derr != nil {
		return "", st.encode(), fmt.Errorf("parse requirements: %w", derr)
	}
	if reqData.Token == "" {
		return "", st.encode(), fmt.Errorf("token requirements kosong (mungkin diblokir)")
	}
	powToken := solvePow(reqData.ProofOfWork.Seed, reqData.ProofOfWork.Difficulty)

	// 2) Conversation (SSE).
	msgID := uuid.NewString()
	body := map[string]interface{}{
		"action": "next",
		"messages": []interface{}{map[string]interface{}{
			"id":       msgID,
			"author":   map[string]interface{}{"role": "user"},
			"content":  map[string]interface{}{"content_type": "text", "parts": []string{prompt}},
			"metadata": map[string]interface{}{},
		}},
		"parent_message_id":             st.ParentMessageID,
		"model":                         "auto",
		"timezone_offset_min":           -420,
		"history_and_training_disabled": true,
		"conversation_mode":             map[string]interface{}{"kind": "primary_assistant"},
		"websocket_request_id":          uuid.NewString(),
	}
	if st.ConvID != "" {
		body["conversation_id"] = st.ConvID
	}

	cq, _ := http.NewRequest(http.MethodPost, chatgptBase+"/conversation", bytes.NewReader(mustJSON(body)))
	chatgptHeaders(cq, st.DeviceID, "application/json", "text/event-stream")
	cq.Header.Set("openai-sentinel-chat-requirements-token", reqData.Token)
	cq.Header.Set("openai-sentinel-proof-token", powToken)

	cr, err := client.Do(cq)
	if err != nil {
		return "", st.encode(), fmt.Errorf("conversation: %w", err)
	}
	defer cr.Body.Close()
	if cr.StatusCode == 403 || cr.StatusCode == 429 {
		return "", st.encode(), fmt.Errorf("conversation ditolak (status %d)", cr.StatusCode)
	}

	answer := ""
	sc := bufio.NewScanner(cr.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(line[6:])
		if payload == "[DONE]" {
			break
		}
		var ev cgptEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		if ev.ConversationID != "" {
			st.ConvID = ev.ConversationID
		}
		if ev.Message == nil || ev.Message.Author.Role != "assistant" {
			continue
		}
		if len(ev.Message.Content.Parts) > 0 {
			var part string
			if err := json.Unmarshal(ev.Message.Content.Parts[0], &part); err == nil && part != "" {
				answer = part // tiap chunk = teks kumulatif
			}
		}
		if ev.Message.ID != "" {
			st.ParentMessageID = ev.Message.ID
		}
	}
	if answer == "" {
		return "", st.encode(), fmt.Errorf("tidak ada jawaban dari ChatGPT")
	}
	return answer, st.encode(), nil
}

func chatgptHeaders(r *http.Request, deviceID, contentType, accept string) {
	r.Header.Set("user-agent", chatgptUA)
	r.Header.Set("content-type", contentType)
	r.Header.Set("accept", accept)
	r.Header.Set("oai-device-id", deviceID)
	r.Header.Set("oai-language", "en-US")
	r.Header.Set("origin", "https://chatgpt.com")
	r.Header.Set("referer", "https://chatgpt.com/")
}

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
