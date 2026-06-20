package src // Sesuaikan dengan nama package Anda

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Sticker (Fungsi kosong yang disiapkan)
func Sticker() (*APIResult, error) {
	// Anda bisa mengisinya nanti, contoh:
	// endpoint := "/d/tools/sticker"
	// return fetchAPI(endpoint)
	
	return nil, fmt.Errorf("fungsi Sticker belum diimplementasikan")
}