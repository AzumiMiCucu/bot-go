package src

import "testing"

// HTML sintetis meniru struktur SSR Google Lens: tiap hasil = subtree berisi
// SATU URL halaman + judul + thumbnail gstatic (di cabang berbeda).
const glensSampleHTML = `
<script>AF_initDataCallback({key: 'ds:1', hash: '2', data:[
  "header", null,
  [
    [ null, ["Nazuna cute by DonBom", 0], ["https://www.furaffinity.net/view/44566501/", 12], [ ["https://encrypted-tbn0.gstatic.com/images?q=tbn:ABC", 120, 90] ] ],
    [ null, ["Post by Mofulyen on X", 0], ["https://x.com/MofulyenS/status/2046499427628589296", 3], [ ["https://encrypted-tbn0.gstatic.com/images?q=tbn:XYZ", 100, 100] ] ]
  ]
], sideChannel: {}});</script>`

func TestGlensParseAF(t *testing.T) {
	res := glensParseAF(glensSampleHTML)
	res = glensDedupe(res)
	if len(res) != 2 {
		t.Fatalf("mau 2 hasil, dapat %d: %+v", len(res), res)
	}
	got := map[string]GLensResult{}
	for _, r := range res {
		got[r.Domain] = r
	}
	fa, ok := got["furaffinity.net"]
	if !ok {
		t.Fatalf("furaffinity tidak ada: %+v", res)
	}
	if fa.Title != "Nazuna cute by DonBom" {
		t.Errorf("title salah: %q", fa.Title)
	}
	if fa.Source != "https://www.furaffinity.net/view/44566501/" {
		t.Errorf("source salah: %q", fa.Source)
	}
	if fa.Thumbnail == "" || fa.Thumbnail != "https://encrypted-tbn0.gstatic.com/images?q=tbn:ABC" {
		t.Errorf("thumbnail salah/kosong: %q", fa.Thumbnail)
	}
	x, ok := got["x.com"]
	if !ok || x.Thumbnail == "" {
		t.Errorf("x.com thumbnail kosong: %+v", x)
	}
}
