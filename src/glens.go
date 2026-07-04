package src

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
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
//      ke www.google.com/search?...&udm=26 (session Lens ada di URL).
//   2. GET URL redirect itu DENGAN cookie Google PENUH (login) → HTML ter-SSR
//      yang menaruh data hasil di blok `AF_initDataCallback([...])`.
//   3. Parse blok itu sebagai JSON, deep-walk STRUKTUR-AGNOSTIK: turun sampai
//      menemukan subtree berisi TEPAT SATU URL halaman (= satu hasil), lalu
//      ambil judul + thumbnail dari seluruh isi subtree tersebut.
//
// PENTING: butuh cookie Google PENUH & valid (owner set via `setnid`). Cookie
// NID saja sering tak cukup → hasil kosong / data tak lengkap. Tanpa SSR, Google
// balas shell "enablejs".

const glensUploadBase = "https://lens.google.com/v3/upload"

// UA browser desktop yang ditiru dari capture request asli.
const glensUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

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
	Title        string // judul/teks tautan sumber
	Source       string // URL halaman tempat gambar muncul
	Domain       string // domain sumber (tanpa www.)
	Thumbnail    string // data URI base64 thumbnail (di-SSR Google) bila ada
	ThumbnailURL string // URL http publik gstatic/encrypted-tbn (bisa di-fetch WA)
}

// glensCookie mengembalikan cookie yang dipakai untuk request (cookie penuh
// diprioritaskan; fallback ke "NID=..." lama demi kompatibilitas config lama).
func glensCookie() string {
	if AppConfig == nil {
		return ""
	}
	if c := strings.TrimSpace(AppConfig.GoogleCookie); c != "" {
		return c
	}
	/*if nid := strings.TrimSpace(AppConfig.GoogleNID); nid != "" {
		return "NID=" + strings.TrimPrefix(nid, "NID=")
	}*/
	return ""
}

// GoogleLensSearch mengirim gambar ke Google Lens dan mengembalikan daftar hasil.
// Membutuhkan cookie Google penuh & valid (AppConfig.GoogleCookie).
func GoogleLensSearch(imageData []byte) ([]GLensResult, error) {
	if len(imageData) == 0 {
		return nil, fmt.Errorf("data gambar kosong")
	}
	cookie := glensCookie()
	if cookie == "" {
		return nil, fmt.Errorf("cookie Google belum diset — owner set dulu via `setnid <cookie penuh>`")
	}
	// Stiker WA = WebP → konversi ke JPEG di memori.
	imageData, err := ToJPEGForAPI(imageData)
	if err != nil {
		return nil, err
	}

	// 1) Upload → ambil Location (URL hasil dengan session Lens).
	searchURL, err := glensUpload(imageData, cookie)
	if err != nil {
		return nil, err
	}

	// 2) Fetch halaman hasil dengan cookie penuh.
	htmlDoc, err := glensFetchResults(searchURL, cookie)
	if err != nil {
		return nil, err
	}

	// 3) Parse: utamakan deep-walk AF_initDataCallback, fallback anchor.
	results := glensParseAF(htmlDoc)
	if len(results) == 0 {
		results = glensParseAnchors(htmlDoc)
	}
	return glensDedupe(results), nil
}

// glensUpload POST gambar, balikan URL redirect (Location) berisi session hasil.
func glensUpload(imageData []byte, cookie string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// PENTING: part `encoded_image` HARUS ber-Content-Type image/... —
	// bukan application/octet-stream default `CreateFormFile`. Diverifikasi live
	// 2026-07-04: dengan octet-stream Google balas Location "kurus" (lns_vfs=d)
	// yang cuma memuat shell JS (0 hasil); dengan image/jpeg Google balas Location
	// "kaya" (gsessionid, lns_mode=un) yang ter-SSR penuh berisi data hasil.
	partHdr := make(textproto.MIMEHeader)
	partHdr.Set("Content-Disposition", `form-data; name="encoded_image"; filename="image.jpg"`)
	partHdr.Set("Content-Type", "image/jpeg")
	fw, err := w.CreatePart(partHdr)
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
	u := fmt.Sprintf("%s?ep=gsbubb&st=%d&authuser=0&hl=id&vpw=980&vph=1873", glensUploadBase, ts)
	req, err := http.NewRequest(http.MethodPost, u, &buf)
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

	resp, err := glensHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("Lens upload gagal: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("Lens: tidak mendapat URL hasil (status %d) — cookie mungkin invalid", resp.StatusCode)
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
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	// Follow redirect di sini boleh.
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
		return "", fmt.Errorf("Lens status %d — cookie mungkin kedaluwarsa", resp.StatusCode)
	}
	return string(body), nil
}

// =================================================================
// PARSER A (utama): AF_initDataCallback → array JSON → deep-walk.
// =================================================================

// glensParseAF mengekstrak hasil dari blok AF_initDataCallback([...]).
func glensParseAF(doc string) []GLensResult {
	var out []GLensResult
	const marker = "AF_initDataCallback("
	i := 0
	for {
		p := strings.Index(doc[i:], marker)
		if p < 0 {
			break
		}
		start := i + p + len(marker)
		i = start
		// Cari `data:` lalu array `[` seimbang sesudahnya.
		dp := strings.Index(doc[start:], "data:")
		if dp < 0 {
			continue
		}
		ap := strings.IndexByte(doc[start+dp:], '[')
		if ap < 0 {
			continue
		}
		arrStart := start + dp + ap
		arrStr := glensBalanced(doc, arrStart)
		if arrStr == "" {
			continue
		}
		var node interface{}
		if err := json.Unmarshal([]byte(arrStr), &node); err != nil {
			continue // bagian tak valid JSON dilewati
		}
		glensWalk(node, &out)
	}
	return out
}

// glensWalk mereplikasi persis algoritma parser Node yang TERBUKTI jalan:
// pada TIAP array, lihat HANYA string anak-LANGSUNG (bukan rekursif). Bila ada
// URL eksternal di antaranya → jadikan 1 kandidat hasil (source = URL non-gambar
// pertama, judul & thumbnail dari string anak-langsung yang sama). Lalu tetap
// turun ke semua anak agar hasil bersarang lain ikut terjaring.
func glensWalk(node interface{}, out *[]GLensResult) {
	switch v := node.(type) {
	case []interface{}:
		// Kumpulkan HANYA string anak-langsung (meniru node.filter(typeof==string)).
		var strs []string
		for _, c := range v {
			if s, ok := c.(string); ok {
				strs = append(strs, s)
			}
		}
		// URL eksternal di antara string anak-langsung.
		var urls []string
		for _, s := range strs {
			if glensIsExternalURL(s) {
				urls = append(urls, s)
			}
		}
		if len(urls) > 0 {
			// source: URL non-gambar pertama, fallback URL pertama.
			source := ""
			for _, u := range urls {
				if !glensLooksImage(u) {
					source = u
					break
				}
			}
			if source == "" {
				source = urls[0]
			}
			thumb := ""
			title := ""
			for _, s := range strs {
				if thumb == "" && glensIsThumb(s) {
					thumb = s
				}
				if title == "" && !glensIsURL(s) && len([]rune(s)) >= 4 && len(s) <= 200 &&
					strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
					!glensIsJunk(s) {
					title = strings.TrimSpace(s)
				}
			}
			*out = append(*out, GLensResult{
				Title:     title,
				Source:    source,
				Domain:    glensDomainOf(source),
				Thumbnail: thumb,
			})
		}
		// Tetap telusuri semua anak (meniru rekursi Node setelah push).
		for _, c := range v {
			glensWalk(c, out)
		}
	case map[string]interface{}:
		for _, c := range v {
			glensWalk(c, out)
		}
	}
}

// =================================================================
// PARSER B (fallback): anchor `<a href=".." ping="/url?...">`.
// =================================================================

var glensAnchorRe = regexp.MustCompile(`(?is)<a[^>]*\shref="(https?://[^"]+)"[^>]*\sping="/url\?[^"]*"[^>]*>`)
var glensHeadingRe = regexp.MustCompile(`(?is)role="heading"[^>]*>\s*(?:<[^>]+>\s*)*([^<]{1,180})`)

// Thumbnail Google Lens dikirim sebagai base64 yang di-defer: tiap kartu punya
// `<img id="dimg_XXX_N" data-deferred="1">` (placeholder), lalu script terpisah
// `var s='data:image/jpeg;base64,....';var ii=['dimg_XXX_N'];_setImagesSrc(...)`
// memetakan gambar asli ke id itu. Kita bangun peta id→dataURI lalu pasangkan.
var glensSetImgRe = regexp.MustCompile(`(?s)var s='(data:image[^']*)';var ii=\[([^\]]*)\];`)
var glensImgIDRe = regexp.MustCompile(`id="(dimg_[^"]+)"`)
var glensIDListRe = regexp.MustCompile(`'([^']+)'`)

// glensThumbMap membangun peta id `dimg_...` → data URI gambar (JPEG/PNG/WebP).
func glensThumbMap(doc string) map[string]string {
	m := make(map[string]string)
	for _, mm := range glensSetImgRe.FindAllStringSubmatch(doc, -1) {
		data := mm[1]
		for _, id := range glensIDListRe.FindAllStringSubmatch(mm[2], -1) {
			m[id[1]] = data
		}
	}
	return m
}

func glensParseAnchors(doc string) []GLensResult {
	thumbs := glensThumbMap(doc)
	locs := glensAnchorRe.FindAllStringSubmatchIndex(doc, -1)
	out := make([]GLensResult, 0, len(locs))
	for i, m := range locs {
		link := html.UnescapeString(doc[m[2]:m[3]])
		if !glensIsExternalURL(link) {
			continue
		}
		// Judul: cari role="heading" pada blok SETELAH anchor (s/d anchor berikut).
		end := len(doc)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		fwd := end
		if fwd-m[1] > 2000 {
			fwd = m[1] + 2000
		}
		title := ""
		if tm := glensHeadingRe.FindStringSubmatch(doc[m[1]:fwd]); tm != nil {
			title = strings.TrimSpace(html.UnescapeString(tm[1]))
		}
		// Thumbnail: gambar kartu ada SEBELUM anchor. Telusuri jendela mundur
		// (maks 2600 char) untuk id dimg_, lalu ambil data URI JPEG TERBESAR
		// (favicon kecil / PNG placeholder terabaikan otomatis).
		lo := m[0] - 2600
		if lo < 0 {
			lo = 0
		}
		// Ambil data URI TERBESAR di jendela; syarat >2 KB agar favicon/placeholder
		// mungil (mis. PNG 470 byte) terbuang & hanya thumbnail asli yang terpilih.
		// Utamakan jendela MUNDUR (gambar kartu umumnya sebelum anchor); bila tak
		// ketemu, coba jendela MAJU (sebagian kartu menaruh gambar setelah anchor).
		hi := fwd
		if hi-m[1] > 2600 {
			hi = m[1] + 2600
		}
		thumb := glensBiggestThumb(doc[lo:m[0]], thumbs)
		if thumb == "" {
			thumb = glensBiggestThumb(doc[m[1]:hi], thumbs)
		}
		// URL thumbnail publik (encrypted-tbn.gstatic.com) — bisa di-fetch WA
		// langsung sehingga TAMPIL di kartu AiRich tanpa perlu upload/host.
		thumbURL := glensFirstTbnURL(doc[lo:m[0]])
		if thumbURL == "" {
			thumbURL = glensFirstTbnURL(doc[m[1]:hi])
		}
		out = append(out, GLensResult{Title: title, Source: link, Domain: glensDomainOf(link), Thumbnail: thumb, ThumbnailURL: thumbURL})
	}
	return out
}

// glensBiggestThumb mengembalikan data URI TERBESAR (>2 KB) dari id dimg_ yang
// ditemukan pada potongan `seg`, atau "" bila tak ada.
func glensBiggestThumb(seg string, thumbs map[string]string) string {
	best := ""
	for _, idm := range glensImgIDRe.FindAllStringSubmatch(seg, -1) {
		if d, ok := thumbs[idm[1]]; ok && len(d) > 2000 && len(d) > len(best) {
			best = d
		}
	}
	return best
}

// glensTbnURLRe menangkap URL thumbnail publik gstatic (tanpa escape HTML).
var glensTbnURLRe = regexp.MustCompile(`https://encrypted-tbn[0-9]\.gstatic\.com/images\?q=tbn:[^"'\\ ]+`)

// glensFirstTbnURL mengembalikan URL encrypted-tbn pertama pada potongan `seg`.
func glensFirstTbnURL(seg string) string {
	return glensTbnURLRe.FindString(seg)
}

// GLensThumbBytes mendekode thumbnail. Bila `thumb` berupa data URI
// (`data:image/...;base64,...`) → kembalikan byte + mime hasil dekode. Bila URL
// http(s) → unduh. ok=false bila kosong/gagal.
func GLensThumbBytes(thumb string) ([]byte, string, bool) {
	thumb = strings.TrimSpace(thumb)
	if thumb == "" {
		return nil, "", false
	}
	if strings.HasPrefix(thumb, "data:") {
		semi := strings.IndexByte(thumb, ',')
		if semi < 0 {
			return nil, "", false
		}
		meta, payload := thumb[5:semi], thumb[semi+1:]
		mime := "image/jpeg"
		if p := strings.IndexByte(meta, ';'); p > 0 {
			mime = meta[:p]
		}
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || len(data) == 0 {
			return nil, "", false
		}
		return data, mime, true
	}
	data, ctype, err := DownloadBytes(thumb)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	if ctype == "" {
		ctype = "image/jpeg"
	}
	return data, ctype, true
}

// =================================================================
// Util.
// =================================================================

// glensBalanced mengembalikan substring `[...]` seimbang mulai dari indeks '['.
func glensBalanced(s string, start int) string {
	depth := 0
	inStr := false
	esc := false
	for k := start; k < len(s); k++ {
		c := s[k]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[start : k+1]
			}
		}
	}
	return ""
}

func glensIsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

var glensInternalRe = regexp.MustCompile(`(?i)^https?://(?:[a-z0-9-]+\.)*(google\.com|gstatic\.com|googleusercontent\.com|googleapis\.com|youtube\.com|ggpht\.com|schema\.org)(/|$)`)

func glensIsExternalURL(s string) bool {
	return glensIsURL(s) && !glensInternalRe.MatchString(s)
}

var glensImgExtRe = regexp.MustCompile(`(?i)\.(jpg|jpeg|png|webp|gif|svg)(\?|$)`)

func glensLooksImage(u string) bool {
	return glensImgExtRe.MatchString(u) || strings.Contains(u, "encrypted-tbn") || strings.Contains(u, "gstatic.com")
}

// glensIsThumb: URL yang layak jadi thumbnail (gstatic/tbn atau berekstensi gambar).
func glensIsThumb(s string) bool {
	if !glensIsURL(s) {
		return strings.HasPrefix(s, "data:image/")
	}
	return strings.Contains(s, "encrypted-tbn") || strings.Contains(s, "gstatic.com") || glensImgExtRe.MatchString(s)
}

var (
	glensJunkHashRe = regexp.MustCompile(`^[A-Za-z0-9_+/=-]{25,}$`)
	glensJunkPreRe  = regexp.MustCompile(`(?i)^(ds:|GRID|SDCH|data:|rgb|#[0-9a-f]{3,8})`)
	glensJunkNumRe  = regexp.MustCompile(`^[\d.,\s-]+$`)
)

func glensIsJunk(s string) bool {
	return glensJunkHashRe.MatchString(s) || glensJunkPreRe.MatchString(s) || glensJunkNumRe.MatchString(s)
}

func glensDomainOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// glensDedupe membuang hasil duplikat berdasar URL sumber (tanpa query/fragment).
func glensDedupe(list []GLensResult) []GLensResult {
	seen := make(map[string]bool)
	out := make([]GLensResult, 0, len(list))
	for _, r := range list {
		if r.Source == "" {
			continue
		}
		key := r.Source
		if idx := strings.IndexAny(key, "?#"); idx >= 0 {
			key = key[:idx]
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}
