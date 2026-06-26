package src // Sesuaikan dengan nama package Anda

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const BaseURL = "https://ps.azumi.dev"

// PSBaseURL adalah base URL API pribadi ps.azumi.dev
const PSBaseURL = "https://ps.azumi.dev"

/* API BY @azumimicucu (Translated to Go) */

// FetchPS adalah helper global untuk memanggil endpoint API ps.azumi.dev.
// Contoh: FetchPS("/d/finder/ytmusic?q=...")
func FetchPS(endpoint string) (*APIResult, error) {
	fullURL := PSBaseURL + endpoint

	resp, err := http.Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("gagal request ke ps.azumi.dev: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gagal membaca response: %w", err)
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("gagal parsing JSON: %w", err)
	}

	return &APIResult{Status: resp.StatusCode, Data: data}, nil
}

// DownloadBytes mengunduh file dari URL apa pun menjadi []byte beserta content-type-nya.
func DownloadBytes(rawURL string) ([]byte, string, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("gagal mengunduh: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status unduhan tidak OK: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("gagal membaca body: %w", err)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// YtMusicSearch mencari lagu di YouTube Music.
func YtMusicSearch(query string) (*APIResult, error) {
	return FetchPS("/d/finder/ytmusic?q=" + url.QueryEscape(query))
}

// YtMp3 mengambil tautan unduhan mp3 dari sebuah URL music.youtube.com.
func YtMp3(musicURL string) (*APIResult, error) {
	return FetchPS("/d/fetcher/ytmp3?url=" + url.QueryEscape(musicURL))
}

// APIResult merepresentasikan struktur { status: 200, data: ... }
// Kita menggunakan interface{} untuk Data karena struktur JSON dari masing-masing API bisa berbeda.
type APIResult struct {
	Status int         `json:"status"`
	Data   interface{} `json:"data"`
}

// fetchAPI adalah fungsi helper internal untuk melakukan HTTP GET request
func fetchAPI(endpoint string) (*APIResult, error) {
	fullURL := BaseURL + endpoint

	resp, err := http.Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("gagal melakukan request ke API: %w", err)
	}
	defer resp.Body.Close() // Wajib di Golang untuk mencegah memory leak

	// Membaca seluruh response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gagal membaca response: %w", err)
	}

	// Parsing JSON ke dalam interface{}
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("gagal parsing JSON: %w", err)
	}

	return &APIResult{
		Status: resp.StatusCode,
		Data:   data,
	}, nil
}

// WxGpt mengirimkan prompt ke API wxGpt
func WxGpt(question string) (*APIResult, error) {
	// url.QueryEscape digunakan agar spasi atau karakter khusus aman saat dikirim di URL
	endpoint := fmt.Sprintf("/d/auto/wxgpt?prompt=%s", url.QueryEscape(question))
	return fetchAPI(endpoint)
}

func Gemini(question string) (*APIResult, error) {
	// url.QueryEscape digunakan agar spasi atau karakter khusus aman saat dikirim di URL
	endpoint := fmt.Sprintf("/d/auto/gemini-3.1-flash?prompt=%s", url.QueryEscape(question))
	return fetchAPI(endpoint)
}

// SpotifyDL mengambil data dari link Spotify
func SpotifyDL(link string) (*APIResult, error) {
	endpoint := fmt.Sprintf("/d/fetcher/spotify?url=%s", url.QueryEscape(link))
	return fetchAPI(endpoint)
}

// IgStalk mengambil data profil Instagram berdasarkan username
func IgStalk(username string) (*APIResult, error) {
	endpoint := fmt.Sprintf("/d/finder/igstalk?username=%s", url.QueryEscape(username))
	return fetchAPI(endpoint)
}

// =================================================================
// INSTAGRAM DOWNLOADER — /d/fetcher/sssinstagram
// =================================================================
// Endpoint mengembalikan { success, result } dengan `result` yang bisa berupa:
//   - OBJECT  : satu media (mis. reel/video tunggal)
//   - ARRAY   : banyak media (postingan carousel/multi-foto)
// Tiap entri punya `url` (array kualitas) + `meta`. Helper ini MENORMALKAN
// keduanya menjadi satu daftar IgMediaItem yang mudah dikirim ke WhatsApp.

// IgMediaItem = satu media siap unduh dari hasil Instagram.
type IgMediaItem struct {
	URL     string // tautan unduhan langsung
	Type    string // mp4 / jpg / ... (dari API)
	Ext     string // ekstensi file
	Quality int    // kualitas (mis. 720, 1080) — 0 bila tak ada
	IsVideo bool   // true bila video (mp4)
}

// IgDownloadResult = ringkasan hasil unduhan Instagram.
type IgDownloadResult struct {
	Items    []IgMediaItem
	Title    string
	Username string
	Source   string
}

// struktur mentah respons sssinstagram.
type ssURL struct {
	URL     string `json:"url"`
	Type    string `json:"type"`
	Ext     string `json:"ext"`
	Quality int    `json:"quality"`
}
type ssEntry struct {
	URL  []ssURL `json:"url"`
	Meta struct {
		Title    string `json:"title"`
		Username string `json:"username"`
		Source   string `json:"source"`
	} `json:"meta"`
}
type ssResp struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
}

// IgDownload memanggil API sssinstagram untuk sebuah URL Instagram (reel/post)
// dan mengembalikan daftar media yang sudah dinormalkan.
func IgDownload(igURL string) (*IgDownloadResult, error) {
	endpoint := PSBaseURL + "/d/fetcher/sssinstagram?url=" + url.QueryEscape(igURL)

	resp, err := toolHTTP.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("gagal request Instagram: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gagal membaca respons: %w", err)
	}

	var r ssResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("gagal parsing JSON Instagram: %w", err)
	}
	if !r.Success {
		if r.Error != "" {
			return nil, fmt.Errorf("API Instagram: %s", r.Error)
		}
		return nil, fmt.Errorf("API Instagram mengembalikan gagal")
	}

	// `result` bisa ARRAY (multi-media) atau OBJECT (media tunggal).
	var entries []ssEntry
	if err := json.Unmarshal(r.Result, &entries); err != nil {
		var single ssEntry
		if err2 := json.Unmarshal(r.Result, &single); err2 != nil {
			return nil, fmt.Errorf("format result Instagram tak dikenal")
		}
		entries = []ssEntry{single}
	}

	out := &IgDownloadResult{}
	for _, e := range entries {
		best := pickBestIgURL(e.URL)
		if best.URL == "" {
			continue
		}
		isVideo := strings.EqualFold(best.Type, "mp4") || strings.EqualFold(best.Ext, "mp4")
		out.Items = append(out.Items, IgMediaItem{
			URL:     best.URL,
			Type:    best.Type,
			Ext:     best.Ext,
			Quality: best.Quality,
			IsVideo: isVideo,
		})
		if out.Title == "" {
			out.Title = e.Meta.Title
		}
		if out.Username == "" {
			out.Username = e.Meta.Username
		}
		if out.Source == "" {
			out.Source = e.Meta.Source
		}
	}

	if len(out.Items) == 0 {
		return nil, fmt.Errorf("tidak ada media yang bisa diunduh")
	}
	return out, nil
}

// pickBestIgURL memilih kualitas terbaik dari daftar url sebuah entri (quality
// tertinggi). Bila semua quality 0, ambil yang pertama.
func pickBestIgURL(urls []ssURL) ssURL {
	var best ssURL
	for i, u := range urls {
		if u.URL == "" {
			continue
		}
		if i == 0 || u.Quality > best.Quality {
			best = u
		}
	}
	return best
}

// =================================================================
// TOOLS ps.azumi.dev — endpoint yang menerima FILE (multipart) dan
// mengembalikan BINARY mentah (bukan JSON). Mis. sticker & removebg.
// =================================================================

var toolHTTP = &http.Client{Timeout: 120 * time.Second}

// PostFileTool mengunggah `data` sebagai field multipart "file" ke endpoint tools
// (mis. "/d/tools/sticker"), menyertakan field teks tambahan (author/pack/dll),
// lalu mengembalikan body respons mentah + content-type. Error bila status != 2xx
// (body biasanya JSON {success:false,error:...} dari API).
func PostFileTool(path, filename string, data []byte, fields map[string]string) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err = fw.Write(data); err != nil {
		return nil, "", err
	}
	for k, v := range fields {
		if v != "" {
			_ = w.WriteField(k, v)
		}
	}
	if err = w.Close(); err != nil {
		return nil, "", err
	}

	req, err := http.NewRequest(http.MethodPost, PSBaseURL+path, &buf)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := toolHTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.Header.Get("Content-Type"), fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// MakeSticker mengirim buffer gambar/video/webp ke /d/tools/sticker dan
// mengembalikan buffer WebP stiker (sudah ber-metadata author/pack dari API).
func MakeSticker(data []byte, filename, author, pack string) ([]byte, string, error) {
	return PostFileTool("/d/tools/sticker", filename, data, map[string]string{
		"author": author,
		"pack":   pack,
	})
}

// RemoveBg mengirim buffer gambar ke /d/tools/removebg dan mengembalikan
// buffer PNG dengan latar transparan.
func RemoveBg(data []byte, filename string) ([]byte, string, error) {
	return PostFileTool("/d/tools/removebg", filename, data, nil)
}
