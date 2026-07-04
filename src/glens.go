package src

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// =================================================================
// GOOGLE LENS — reverse image search via lens.google.com (fitur `glens`).
// =================================================================
// Alur (diverifikasi live 2026-07-04):
//   1. POST multipart gambar (field `encoded_image`) ke
//      https://lens.google.com/v3/upload?ep=gsbubb&st=<ms>&... → 303 redirect
//      ke www.google.com/search?vsrid=... (session Lens ada di URL).
//   2. GET URL redirect itu DENGAN cookie Google `NID` yang valid → HTML 1MB
//      berisi hasil ter-render sebagai DOM (BUKAN AF_initDataCallback lagi —
//      pendekatan skrip lama sudah usang & dihapus Google).
//   3. Parse tiap hasil dari anchor `<a href=".." ping="/url?..">` + heading
//      (title) + label sumber (domain).
//
// PENTING: tanpa cookie NID valid, Google hanya balas shell JS (0 hasil).
// NID disimpan di config (AppConfig.GoogleNID), diperbarui owner via `setnid`.

const glensUploadBase = "https://lens.google.com/v3/upload"

// UA browser desktop-mobile yang ditiru dari capture request asli (com.xbrowser.play).
const glensUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// Klien khusus glens: TIDAK auto-follow redirect (kita butuh Location upload).
var glensHTTP = &http.Client{
	Timeout:   60 * time.Second,
	Transport: sharedTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // hentikan di redirect pertama
	},
}

// GLensResult = satu hasil Google Lens.
type GLensResult struct {
	Title  string
	Domain string
	Link   string
}

var (
	glensAnchorRe = regexp.MustCompile(`(?is)<a[^>]*\shref="(https?://[^"]+)"[^>]*\sping="/url\?[^"]*"[^>]*>`)
	glensTitleRe  = regexp.MustCompile(`(?is)role="heading"[^>]*>\s*(?:<[^>]+>\s*)*([^<]{1,180})`)
	glensDomainRe = regexp.MustCompile(`(?is)<div class="R8BTeb[^"]*">([^<]{1,80})</div>`)
)

// GoogleLensSearch mengirim gambar ke Google Lens dan mengembalikan daftar hasil.
// Membutuhkan AppConfig.GoogleNID (cookie NID Google) yang valid.
func GoogleLensSearch(imageData []byte) ([]GLensResult, error) {
	if len(imageData) == 0 {
		return nil, fmt.Errorf("data gambar kosong")
	}
	nid := ""
	if AppConfig != nil {
		nid = strings.TrimSpace(AppConfig.GoogleNID)
	}
	if nid == "" {
		return nil, fmt.Errorf("cookie Google NID belum diset — owner set dulu via `setnid <cookie>`")
	}
	// Stiker WA = WebP → konversi ke JPEG di memori.
	imageData, err := ToJPEGForAPI(imageData)
	if err != nil {
		return nil, err
	}
	cookie := "NID=" + nid

	// 1) Upload → ambil Location (URL hasil dengan session Lens).
	searchURL, err := glensUpload(imageData, cookie)
	if err != nil {
		return nil, err
	}

	// 2) Fetch halaman hasil dengan cookie NID.
	htmlDoc, err := glensFetchResults(searchURL, cookie)
	if err != nil {
		return nil, err
	}

	// 3) Parse.
	results := glensParse(htmlDoc)
	return results, nil
}

// glensUpload POST gambar, balikan URL redirect (Location) berisi session hasil.
func glensUpload(imageData []byte, cookie string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("encoded_image", "image.jpg")
	if err != nil {
		return "", err
	}
	if _, err = fw.Write(imageData); err != nil {
		return "", err
	}
	if err = w.Close(); err != nil {
		return "", err
	}

	ts := time.Now().UnixMilli()
	url := fmt.Sprintf("%s?ep=gsbubb&st=%d&authuser=0&hl=id&vpw=980&vph=1873", glensUploadBase, ts)
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", glensUA)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", "https://www.google.com")
	req.Header.Set("Referer", "https://www.google.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("x-requested-with", "com.xbrowser.play")
	req.Header.Set("sec-ch-ua-form-factors", `"Mobile"`)
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := glensHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("Lens upload gagal: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("Lens: tidak mendapat URL hasil (status %d) — cookie NID mungkin invalid", resp.StatusCode)
	}
	return loc, nil
}

// glensFetchResults GET halaman hasil, balikan HTML.
func glensFetchResults(searchURL, cookie string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, searchURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", glensUA)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", "https://lens.google.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("sec-fetch-mode", "navigate")
	req.Header.Set("sec-fetch-dest", "document")

	// Follow redirect di sini boleh (pakai klien default global lewat transport).
	resp, err := (&http.Client{Timeout: 60 * time.Second, Transport: sharedTransport}).Do(req)
	if err != nil {
		return "", fmt.Errorf("Lens fetch hasil gagal: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("Lens: gagal baca hasil: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Lens status %d — cookie NID mungkin kedaluwarsa", resp.StatusCode)
	}
	return string(body), nil
}

// glensParse mengekstrak hasil dari HTML halaman Lens.
func glensParse(doc string) []GLensResult {
	locs := glensAnchorRe.FindAllStringSubmatchIndex(doc, -1)
	out := make([]GLensResult, 0, len(locs))
	seen := make(map[string]bool)

	for i, m := range locs {
		// m[2],m[3] = grup 1 (href). Blok = dari akhir anchor s/d anchor berikutnya.
		link := html.UnescapeString(doc[m[2]:m[3]])
		if glensSkipLink(link) || seen[link] {
			continue
		}
		start := m[1]
		end := len(doc)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if end-start > 2000 {
			end = start + 2000 // batasi jendela pencarian per hasil
		}
		block := doc[start:end]

		title := ""
		if tm := glensTitleRe.FindStringSubmatch(block); tm != nil {
			title = strings.TrimSpace(html.UnescapeString(tm[1]))
		}
		domain := ""
		if dm := glensDomainRe.FindStringSubmatch(block); dm != nil {
			domain = strings.TrimSpace(html.UnescapeString(dm[1]))
		}
		if title == "" && domain == "" {
			continue
		}
		seen[link] = true
		out = append(out, GLensResult{Title: title, Domain: domain, Link: link})
	}
	return out
}

// glensSkipLink membuang tautan bantuan/internal Google (bukan hasil sungguhan).
func glensSkipLink(link string) bool {
	low := strings.ToLower(link)
	switch {
	case strings.Contains(low, "support.google.com"),
		strings.Contains(low, "accounts.google.com"),
		strings.Contains(low, "policies.google.com"),
		strings.Contains(low, "google.com/search"),
		strings.Contains(low, "/preferences"),
		strings.Contains(low, "/setprefs"):
		return true
	}
	return false
}
