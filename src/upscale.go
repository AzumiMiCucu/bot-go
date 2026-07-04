package src

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"
)

// =================================================================
// WAIFU2X / UPSCALE HD — api.waifu2x.pro
// Port dari skrip JS upscaleImage. Alur:
//  1. POST multipart /api/v1/upscale  (field: denoise,format,type,scale,file)
//     → { hash }
//  2. Poll GET /api/v1/check?hash=<hash> tiap 500ms sampai isFinished == true
//  3. GET /api/v1/get?hash=<hash>&format=<format> → BINARY gambar hasil.
// Endpoint ini bukan ps.azumi.dev, jadi memakai header browser-mobile khusus.
// =================================================================

const waifu2xBase = "https://api.waifu2x.pro"

// header browser-mobile yang ditiru dari skrip asli (situs waifu2x.pro).
var waifu2xHeaders = map[string]string{
	"User-Agent":         "Mozilla/5.0 (Linux; Android 16; Infinix X6837 Build/BP2A.250605.031.A2) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.7778.215 Mobile Safari/537.36",
	"sec-ch-ua-platform": `"Android"`,
	"sec-ch-ua":          `"Chromium";v="148", "Android WebView";v="148", "Not/A)Brand";v="99"`,
	"sec-ch-ua-mobile":   "?1",
	"origin":             "https://waifu2x.pro",
	"x-requested-with":   "com.xbrowser.play",
	"referer":            "https://waifu2x.pro/",
}

// waifu2xHTTP = klien khusus waifu2x (transport bersama, timeout sedang).
var waifu2xHTTP = &http.Client{Timeout: 120 * time.Second, Transport: sharedTransport}

// UpscaleOptions = opsi peningkatan kualitas. Nilai default mengikuti skrip asli.
type UpscaleOptions struct {
	Denoise string // "0".."3" (default "3")
	Format  string // "JPEG" | "PNG" (default "JPEG")
	Type    string // "ANIME" | "PHOTO" (default "ANIME")
	Scale   string // "true" | "false" (default "true")
}

func (o UpscaleOptions) withDefaults() UpscaleOptions {
	if o.Denoise == "" {
		o.Denoise = "3"
	}
	if o.Format == "" {
		o.Format = "JPEG"
	}
	if o.Type == "" {
		o.Type = "ANIME"
	}
	if o.Scale == "" {
		o.Scale = "true"
	}
	return o
}

// UpscaleImage meningkatkan resolusi/kualitas sebuah gambar via waifu2x.pro.
// Mengembalikan buffer gambar hasil (sesuai opts.Format) atau error.
func UpscaleImage(imageData []byte, opts UpscaleOptions) ([]byte, error) {
	if len(imageData) == 0 {
		return nil, fmt.Errorf("data gambar kosong")
	}
	opts = opts.withDefaults()

	// Stiker WA berformat WebP → konversi ke JPEG di memori (waifu2x tolak WebP).
	imageData, err := ToJPEGForAPI(imageData)
	if err != nil {
		return nil, err
	}

	// 1) Upload + minta proses.
	hash, err := waifu2xUpload(imageData, opts)
	if err != nil {
		return nil, err
	}

	// 2) Poll sampai selesai (batasi agar tidak menggantung selamanya).
	deadline := time.Now().Add(100 * time.Second)
	for {
		done, err := waifu2xCheck(hash)
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("waifu2x: proses melebihi batas waktu")
		}
		time.Sleep(500 * time.Millisecond)
	}

	// 3) Ambil hasil.
	return waifu2xDownload(hash, opts.Format)
}

// waifu2xUpload mengunggah gambar sebagai field "file" + opsi, balikan hash.
func waifu2xUpload(imageData []byte, opts UpscaleOptions) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("denoise", opts.Denoise)
	_ = w.WriteField("format", opts.Format)
	_ = w.WriteField("type", opts.Type)
	_ = w.WriteField("scale", opts.Scale)
	fw, err := w.CreateFormFile("file", "image.jpg")
	if err != nil {
		return "", err
	}
	if _, err = fw.Write(imageData); err != nil {
		return "", err
	}
	if err = w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, waifu2xBase+"/api/v1/upscale", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	waifu2xSetHeaders(req)

	resp, err := waifu2xHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("waifu2x upload gagal: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("waifu2x upload status %d", resp.StatusCode)
	}

	var r struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Hash == "" {
		return "", fmt.Errorf("waifu2x: hash tidak diterima")
	}
	return r.Hash, nil
}

// waifu2xCheck mengecek apakah proses hash sudah selesai.
func waifu2xCheck(hash string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet, waifu2xBase+"/api/v1/check?hash="+url.QueryEscape(hash), nil)
	if err != nil {
		return false, err
	}
	waifu2xSetHeaders(req)

	resp, err := waifu2xHTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("waifu2x check gagal: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("waifu2x check status %d", resp.StatusCode)
	}

	var r struct {
		IsFinished bool `json:"isFinished"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return false, fmt.Errorf("waifu2x check: respons tak valid")
	}
	return r.IsFinished, nil
}

// waifu2xDownload mengambil gambar hasil sebagai []byte.
func waifu2xDownload(hash, format string) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/api/v1/get?hash=%s&format=%s", waifu2xBase, url.QueryEscape(hash), url.QueryEscape(format))
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	waifu2xSetHeaders(req)

	resp, err := waifu2xHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("waifu2x download gagal: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("waifu2x download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("waifu2x: gagal membaca hasil: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("waifu2x: hasil kosong")
	}
	return data, nil
}

func waifu2xSetHeaders(req *http.Request) {
	for k, v := range waifu2xHeaders {
		req.Header.Set(k, v)
	}
}
