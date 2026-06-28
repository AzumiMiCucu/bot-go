package src

import (
	"encoding/base64"
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

// =================================================================
// GEMINI (gemini.google.com) — port Go dari skrip JS asli (fetch biasa).
// =================================================================
// Alur:
//  1. GET homepage → ambil token SNlM0e (at), cfb2h (bl), FdrFJe (f.sid) + cookie.
//  2. Susun payload protokol-array BardFrontendService, POST ke StreamGenerate.
//  3. Parse baris `[["wrb.fr",...]]`: ambil teks balasan + state (convId/respId/
//     choiceId/tok) untuk kelanjutan percakapan.
//
// State percakapan (convId/respId/choiceId/tok + cookie) dikodekan base64 JSON,
// kompatibel semangatnya dengan versi JS (token `s` selalu di-fetch ulang).
// =================================================================

const geminiUA = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Mobile Safari/537.36"

var geminiHTTP = &http.Client{Timeout: 60 * time.Second}

var (
	reSNlM0e = regexp.MustCompile(`"SNlM0e":"(.*?)"`)
	reCfb2h  = regexp.MustCompile(`"cfb2h":"(.*?)"`)
	reFdrFJe = regexp.MustCompile(`"FdrFJe":"(.*?)"`)
)

type geminiState struct {
	R         int               `json:"r"`
	ConvID    string            `json:"convId"`
	RespID    string            `json:"respId"`
	ChoiceID  string            `json:"choiceId"`
	Tok       string            `json:"tok"`
	CookieObj map[string]string `json:"cookieObj"`
}

func decodeGeminiState(s string) geminiState {
	st := geminiState{R: 1, CookieObj: map[string]string{}}
	if s == "" {
		return st
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	if st.R <= 0 {
		st.R = 1
	}
	if st.CookieObj == nil {
		st.CookieObj = map[string]string{}
	}
	return st
}

func (st geminiState) encode() string {
	b, _ := json.Marshal(st)
	return base64.StdEncoding.EncodeToString(b)
}

// serializeCookies merangkai map cookie menjadi header "k=v; k2=v2".
func serializeCookies(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// mergeSetCookies menyerap Set-Cookie respons ke dalam map cookie.
func mergeSetCookies(dst map[string]string, h http.Header) {
	for _, c := range h["Set-Cookie"] {
		pair := c
		if i := strings.Index(c, ";"); i >= 0 {
			pair = c[:i]
		}
		if eq := strings.Index(pair, "="); eq > 0 {
			k := strings.TrimSpace(pair[:eq])
			v := strings.TrimSpace(pair[eq+1:])
			if k != "" {
				dst[k] = v
			}
		}
	}
}

func firstSub(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

// GeminiChat mengirim `prompt` ke Gemini. `sys` = instruksi sistem (boleh kosong),
// `prevState` = state percakapan sebelumnya (base64; "" untuk percakapan baru).
// Mengembalikan teks balasan, state baru (untuk disimpan), dan error.
func GeminiChat(prompt, sys, prevState string) (string, string, error) {
	if strings.TrimSpace(sys) == "" {
		sys = "answer calmly and clearly"
	}
	st := decodeGeminiState(prevState)

	// 1) Homepage → token + cookie.
	hreq, _ := http.NewRequest(http.MethodGet, "https://gemini.google.com/", nil)
	hreq.Header.Set("user-agent", geminiUA)
	if len(st.CookieObj) > 0 {
		hreq.Header.Set("cookie", serializeCookies(st.CookieObj))
	}
	hresp, err := geminiHTTP.Do(hreq)
	if err != nil {
		return "", st.encode(), fmt.Errorf("buka gemini: %w", err)
	}
	mergeSetCookies(st.CookieObj, hresp.Header)
	htmlBytes, _ := io.ReadAll(hresp.Body)
	hresp.Body.Close()
	html := string(htmlBytes)

	at := firstSub(reSNlM0e, html)
	bl := firstSub(reCfb2h, html)
	fsid := firstSub(reFdrFJe, html)
	if bl == "" {
		return "", st.encode(), fmt.Errorf("gagal ambil token Gemini (kemungkinan perlu cookie login Google)")
	}

	// 2) Susun payload protokol-array.
	var ctxArr []interface{}
	if st.ConvID != "" {
		ctxArr = []interface{}{st.ConvID, st.RespID, st.ChoiceID, nil, nil, nil, nil, nil, nil, st.Tok}
	} else {
		ctxArr = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	}
	innerMsg := []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	sysArr := []interface{}{"", "", sys, nil, nil, nil, nil, nil, 0, nil, 1, nil, nil, nil, []interface{}{}}
	big := []interface{}{
		innerMsg, []interface{}{"id"}, ctxArr,
		nil, nil, nil, []interface{}{1}, 1, nil, nil, 1, 0, nil, nil, nil, nil, nil, []interface{}{[]interface{}{0}}, 1,
		nil, nil, nil, nil, nil,
		sysArr,
		nil, nil, 1, nil, nil, nil, nil, nil, nil, nil,
		[]interface{}{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		1, nil, nil, nil, nil, []interface{}{1},
	}
	bigJSON, _ := json.Marshal(big)
	pJSON, _ := json.Marshal([]interface{}{nil, string(bigJSON)})

	// 3) POST StreamGenerate.
	q := url.Values{}
	q.Set("bl", bl)
	q.Set("f.sid", fsid)
	q.Set("hl", "id")
	q.Set("_reqid", strconv.Itoa(st.R))
	q.Set("rt", "c")
	st.R++
	endpoint := "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?" + q.Encode()

	form := url.Values{}
	form.Set("f.req", string(pJSON))
	form.Set("at", at)

	preq, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	preq.Header.Set("content-type", "application/x-www-form-urlencoded;charset=UTF-8")
	preq.Header.Set("user-agent", geminiUA)
	preq.Header.Set("x-same-domain", "1")
	preq.Header.Set("cookie", serializeCookies(st.CookieObj))
	presp, err := geminiHTTP.Do(preq)
	if err != nil {
		return "", st.encode(), fmt.Errorf("gemini stream: %w", err)
	}
	mergeSetCookies(st.CookieObj, presp.Header)
	tb, _ := io.ReadAll(presp.Body)
	presp.Body.Close()

	lastText := ""
	for _, ln := range strings.Split(string(tb), "\n") {
		if !strings.HasPrefix(ln, `[["wrb.fr"`) {
			continue
		}
		if t := geminiExtract(ln, &st); t != "" {
			lastText = t
		}
	}
	if lastText == "" {
		return "", st.encode(), fmt.Errorf("tidak ada teks dalam respons Gemini")
	}
	lastText = strings.ReplaceAll(lastText, `\n`, "\n")
	return lastText, st.encode(), nil
}

// geminiExtract mengurai satu baris `[["wrb.fr",...]]`: memperbarui state pada `st`
// dan mengembalikan teks balasan yang ditemukan (kosong bila tak ada).
func geminiExtract(line string, st *geminiState) string {
	var outer []interface{}
	if err := json.Unmarshal([]byte(line), &outer); err != nil || len(outer) == 0 {
		return ""
	}
	row, ok := outer[0].([]interface{})
	if !ok || len(row) < 3 {
		return ""
	}
	payload, ok := row[2].(string)
	if !ok || payload == "" {
		return ""
	}
	var d []interface{}
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		return ""
	}

	// d[1] → convId, respId.
	if len(d) > 1 {
		if a, ok := d[1].([]interface{}); ok && len(a) > 0 {
			if s, ok := a[0].(string); ok && s != "" {
				st.ConvID = s
			}
			if len(a) > 1 {
				if s, ok := a[1].(string); ok && s != "" {
					st.RespID = s
				}
			}
		}
	}
	// d[2] → tok (array[9] atau object["26"]).
	if len(d) > 2 && d[2] != nil {
		if a, ok := d[2].([]interface{}); ok {
			if len(a) > 9 {
				if s, ok := a[9].(string); ok && s != "" {
					st.Tok = s
				}
			}
		} else if m, ok := d[2].(map[string]interface{}); ok {
			if s, ok := m["26"].(string); ok && s != "" {
				st.Tok = s
			}
		}
	}
	// d[4] → choiceId + teks balasan.
	lastText := ""
	if len(d) > 4 {
		if a, ok := d[4].([]interface{}); ok {
			for _, it := range a {
				item, ok := it.([]interface{})
				if !ok {
					continue
				}
				if len(item) > 0 {
					if s, ok := item[0].(string); ok && strings.HasPrefix(s, "rc_") {
						st.ChoiceID = s
					}
				}
				if len(item) > 1 {
					if inner, ok := item[1].([]interface{}); ok && len(inner) > 0 {
						if s, ok := inner[0].(string); ok {
							lastText = s
						}
					}
				}
			}
		}
	}
	return lastText
}
