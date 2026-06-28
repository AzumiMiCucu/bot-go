package src

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// =================================================================
// GOOGLE OAUTH — fondasi integrasi Google Workspace (owner only).
// =================================================================
// Alur sekali-pakai:
//  1. Owner unggah `credentials.json` (OAuth Client dari Google Cloud Console)
//     ke src/secret/credentials.json.
//  2. `gauth` → bot beri URL consent. Owner approve di browser, salin `code`
//     dari URL redirect (http://localhost/?code=...).
//  3. `gauth code <code>` → tukar jadi token (berisi refresh_token), simpan ke
//     database/google_token.json. Setelah itu semua layanan Google siap.
//
// Semua scope diminta sekaligus agar cukup authorize satu kali untuk
// Calendar/Tasks/Gmail/Drive/Docs/Sheets.
// =================================================================

const (
	googleCredFile  = "src/secret/credentials.json"
	googleTokenFile = "src/secret/google_token.json"
	// googleRedirect = loopback; setelah approve, browser ke http://localhost/?code=...
	// (halaman gagal dimuat itu wajar — cukup salin `code` dari address bar).
	googleRedirect = "http://localhost"
)

var googleScopes = []string{
	"https://www.googleapis.com/auth/calendar",
	"https://www.googleapis.com/auth/tasks",
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/drive",
	"https://www.googleapis.com/auth/documents",
	"https://www.googleapis.com/auth/spreadsheets",
}

var googleMu sync.Mutex

func googleConfig() (*oauth2.Config, error) {
	b, err := os.ReadFile(googleCredFile)
	if err != nil {
		return nil, fmt.Errorf("kredensial belum ada — unggah credentials.json ke %s", googleCredFile)
	}
	cfg, err := google.ConfigFromJSON(b, googleScopes...)
	if err != nil {
		return nil, fmt.Errorf("parse credentials.json: %w", err)
	}
	cfg.RedirectURL = googleRedirect
	return cfg, nil
}

// GoogleAuthURL membuat URL consent (offline access + paksa refresh token baru).
func GoogleAuthURL() (string, error) {
	cfg, err := googleConfig()
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL("state-azumi", oauth2.AccessTypeOffline, oauth2.ApprovalForce), nil
}

// GoogleExchange menukar kode otorisasi menjadi token lalu menyimpannya.
func GoogleExchange(code string) error {
	cfg, err := googleConfig()
	if err != nil {
		return err
	}
	tok, err := cfg.Exchange(context.Background(), code)
	if err != nil {
		return fmt.Errorf("tukar kode gagal: %w", err)
	}
	if tok.RefreshToken == "" {
		// Tanpa refresh token, sesi akan kedaluwarsa cepat — minta ulang dgn approval.
		return fmt.Errorf("token tanpa refresh_token — ulangi `gauth` (pastikan approve ulang)")
	}
	return saveGoogleToken(tok)
}

func saveGoogleToken(tok *oauth2.Token) error {
	googleMu.Lock()
	defer googleMu.Unlock()
	b, _ := json.MarshalIndent(tok, "", "  ")
	return os.WriteFile(googleTokenFile, b, 0600)
}

func loadGoogleToken() (*oauth2.Token, error) {
	googleMu.Lock()
	defer googleMu.Unlock()
	b, err := os.ReadFile(googleTokenFile)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// GoogleReady true bila kredensial + token tersimpan sudah ada.
func GoogleReady() bool {
	if _, err := os.Stat(googleCredFile); err != nil {
		return false
	}
	if _, err := os.Stat(googleTokenFile); err != nil {
		return false
	}
	return true
}

// GoogleLogout menghapus token tersimpan (perlu authorize ulang).
func GoogleLogout() error {
	googleMu.Lock()
	defer googleMu.Unlock()
	return os.Remove(googleTokenFile)
}

// GoogleClient mengembalikan *http.Client ber-token (auto-refresh). Bila token
// di-refresh, hasilnya disimpan kembali. Error bila belum terotorisasi.
func GoogleClient(ctx context.Context) (*http.Client, error) {
	cfg, err := googleConfig()
	if err != nil {
		return nil, err
	}
	tok, err := loadGoogleToken()
	if err != nil {
		return nil, fmt.Errorf("belum terotorisasi — jalankan `gauth` dulu")
	}
	ts := cfg.TokenSource(ctx, tok)
	newTok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token gagal (mungkin dicabut) — `gauth` ulang: %w", err)
	}
	if newTok.AccessToken != tok.AccessToken {
		_ = saveGoogleToken(newTok)
	}
	return oauth2.NewClient(ctx, ts), nil
}
