package src // Sesuaikan dengan nama package Anda

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const BaseURL = "https://www.ashema.my.id"

/* API BY @azumimicucu (Translated to Go) */

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