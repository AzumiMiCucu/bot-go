package src

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// =================================================================
// TINEYE — pencarian gambar terbalik (reverse image search / "lens").
// Port dari skrip JS searchImage. POST multipart (field "image") ke
// https://tineye.com/api/v1/result_json/ → JSON daftar kecocokan.
// Bukan ps.azumi.dev, jadi memakai header browser-mobile khusus.
// =================================================================

const tineyeURL = "https://tineye.com/api/v1/result_json/"

var tineyeHeaders = map[string]string{
	"User-Agent":         "Mozilla/5.0 (Linux; Android 16; Infinix X6837 Build/BP2A.250605.031.A2) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.7778.178 Mobile Safari/537.36",
	"Accept":             "application/json, text/plain, */*",
	"sec-ch-ua-platform": `"Android"`,
	"sec-ch-ua":          `"Chromium";v="148", "Android WebView";v="148", "Not/A)Brand";v="99"`,
	"sec-ch-ua-mobile":   "?1",
	"origin":             "https://tineye.com",
	"x-requested-with":   "com.xbrowser.play",
	"sec-fetch-site":     "same-origin",
	"sec-fetch-mode":     "cors",
	"sec-fetch-dest":     "empty",
	"referer":            "https://tineye.com/search",
	"accept-language":    "en-ID,en;q=0.9,id-ID;q=0.8,id;q=0.7,en-US;q=0.6",
	"priority":           "u=1, i",
}

var tineyeHTTP = &http.Client{Timeout: 90 * time.Second, Transport: sharedTransport}

// TinEyeBacklink = satu tautan asal tempat gambar ditemukan.
type TinEyeBacklink struct {
	URL       string `json:"url"`      // halaman web sumber
	Backlink  string `json:"backlink"` // tautan langsung gambar di sumber
	CrawlDate string `json:"crawl_date"`
	ImageName string `json:"image_name"`
}

// TinEyeMatch = satu kecocokan gambar.
type TinEyeMatch struct {
	Domain    string           `json:"domain"`
	Score     float64          `json:"score"`
	Width     int              `json:"width"`
	Height    int              `json:"height"`
	ImageURL  string           `json:"image_url"`
	Backlinks []TinEyeBacklink `json:"backlinks"`

	// Diperkaya manual (lihat enrichment di bawah).
	ResultImageURL string `json:"-"`
}

// TinEyeResult = ringkasan hasil pencarian.
type TinEyeResult struct {
	NumMatches int           // total kecocokan dilaporkan TinEye
	NumStr     string        // teks jumlah ("227 results") bila ada
	TotalPages int           // total halaman hasil
	QueryThumb string        // thumbnail gambar query (bila ada)
	Matches    []TinEyeMatch // daftar kecocokan (halaman ini)
}

// bentuk mentah respons TinEye. Respons ASLI menaruh `matches` & `num_matches`
// di TOP-LEVEL (bukan di dalam `results`). Tetap dukung varian lama `results`
// {matches:[...]} sebagai fallback agar tahan perubahan API.
type tineyeRaw struct {
	QueryHash    string          `json:"query_hash"`
	NumMatches   int             `json:"num_matches"`
	StrNumMatch  string          `json:"str_num_matches"`
	TotalPages   int             `json:"total_pages"`
	Matches      []TinEyeMatch   `json:"matches"`
	Results      json.RawMessage `json:"results"`
	Query        struct {
		Key string `json:"key"`
	} `json:"query"`
	Stats struct {
		QueryHash    string `json:"query_hash"`
		NumMatches   int    `json:"num_matches"`
		TotalMatches int    `json:"total_matches"`
	} `json:"stats"`
}

// TinEyeSearch mengirim gambar ke TinEye dan mengembalikan hasil yang dinormalkan.
func TinEyeSearch(imageData []byte, fileName string) (*TinEyeResult, error) {
	if len(imageData) == 0 {
		return nil, fmt.Errorf("data gambar kosong")
	}
	if fileName == "" {
		fileName = "upload.jpg"
	}

	// Stiker WA berformat WebP → konversi ke JPEG di memori (TinEye tolak WebP).
	imageData, err := ToJPEGForAPI(imageData)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("image", fileName)
	if err != nil {
		return nil, err
	}
	if _, err = fw.Write(imageData); err != nil {
		return nil, err
	}
	// Parameter paging/urutan (sesuai skrip TinEye: offset/limit/sort/order).
	_ = w.WriteField("offset", "0")
	_ = w.WriteField("limit", "20")
	_ = w.WriteField("sort", "score")
	_ = w.WriteField("order", "desc")
	if err = w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, tineyeURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	for k, v := range tineyeHeaders {
		req.Header.Set(k, v)
	}

	resp, err := tineyeHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TinEye request gagal: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("TinEye status %d", resp.StatusCode)
	}

	var raw tineyeRaw
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("TinEye: gagal parsing respons")
	}

	// `matches` ADA di top-level pada respons asli. Fallback: `results` bisa
	// OBJECT {matches:[...]} atau langsung ARRAY [...] pada varian lama.
	matches := raw.Matches
	if len(matches) == 0 && len(raw.Results) > 0 {
		var asObj struct {
			Matches []TinEyeMatch `json:"matches"`
		}
		if err := json.Unmarshal(raw.Results, &asObj); err == nil && asObj.Matches != nil {
			matches = asObj.Matches
		} else {
			_ = json.Unmarshal(raw.Results, &matches)
		}
	}

	out := &TinEyeResult{
		Matches:    matches,
		NumStr:     raw.StrNumMatch,
		TotalPages: raw.TotalPages,
	}

	// Jumlah kecocokan: ambil dari field yang tersedia, fallback panjang slice.
	switch {
	case raw.NumMatches > 0:
		out.NumMatches = raw.NumMatches
	case raw.Stats.NumMatches > 0:
		out.NumMatches = raw.Stats.NumMatches
	case raw.Stats.TotalMatches > 0:
		out.NumMatches = raw.Stats.TotalMatches
	default:
		out.NumMatches = len(matches)
	}

	// Enrichment thumbnail query (sama seperti skrip JS).
	qh := raw.QueryHash
	if qh == "" {
		qh = raw.Stats.QueryHash
	}
	if qh == "" {
		qh = raw.Query.Key
	}
	if qh != "" {
		out.QueryThumb = fmt.Sprintf("https://tineye.com/api/v1/query/%s?size=160", qh)
	}

	return out, nil
}
