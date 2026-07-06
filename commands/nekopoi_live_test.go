package commands

import (
	"os"
	"testing"

	"bot-go/src"
)

// TestNekoLive menguji pipeline nyata: tembus blokir + scraping.
// Butuh jaringan → hanya jalan bila NEKO_LIVE=1 (agar tak mengganggu CI).
// Jalankan: NEKO_LIVE=1 go test -vet=off -run TestNekoLive -v ./commands/
func TestNekoLive(t *testing.T) {
	if os.Getenv("NEKO_LIVE") != "1" {
		t.Skip("set NEKO_LIVE=1 untuk uji jaringan langsung")
	}
	// 1. HOME / TERBARU
	html, err := src.NekoGet(nekopoiBase + "/")
	if err != nil {
		t.Fatalf("NekoGet home gagal: %v", err)
	}
	eps, err := parseNekoLatest(html)
	if err != nil || len(eps) == 0 {
		t.Fatalf("parseNekoLatest gagal: err=%v n=%d", err, len(eps))
	}
	t.Logf("LATEST (%d):", len(eps))
	for i := 0; i < 3 && i < len(eps); i++ {
		t.Logf("  %d. %s\n     url=%s\n     img=%s", i+1, eps[i].Title, eps[i].URL, eps[i].Image)
	}

	// 2. SEARCH
	sh, err := src.NekoGet(nekopoiBase + "/?s=naruto&post_type=anime")
	if err != nil {
		t.Fatalf("NekoGet search gagal: %v", err)
	}
	items, err := parseNekoSearch(sh)
	if err != nil || len(items) == 0 {
		t.Fatalf("parseNekoSearch gagal: err=%v n=%d", err, len(items))
	}
	t.Logf("SEARCH naruto (%d):", len(items))
	for i := 0; i < 3 && i < len(items); i++ {
		t.Logf("  %d. %s", i+1, items[i].Title)
	}

	// 3. DETAIL dari episode terbaru
	dh, err := src.NekoGet(eps[0].URL)
	if err != nil {
		t.Fatalf("NekoGet detail gagal: %v", err)
	}
	d, err := parseNekoDetail(dh)
	if err != nil {
		t.Fatalf("parseNekoDetail gagal: %v", err)
	}
	t.Logf("DETAIL: %s | date=%s views=%s", d.Title, d.Date, d.Views)
	t.Logf("  streams=%d downloads=%d", len(d.Streams), len(d.Downloads))
	for _, s := range d.Streams {
		t.Logf("  stream: %s", s)
	}
	for _, dl := range d.Downloads {
		t.Logf("  [%s] %s (%d link)", dl.Quality, dl.Name, len(dl.Links))
	}

	// 4. Poster bisa diunduh?
	if d.Image != "" {
		img, mime, e := src.NekoDownload(d.Image)
		if e != nil {
			t.Logf("  poster download WARN: %v", e)
		} else {
			t.Logf("  poster OK: %d bytes (%s)", len(img), mime)
		}
	}
}
