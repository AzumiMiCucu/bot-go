package src

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	"golang.org/x/image/webp"
)

// =================================================================
// KONVERSI GAMBAR — utilitas di-memori (tanpa file sementara).
// Stiker WhatsApp berformat WebP, sedangkan API eksternal seperti
// TinEye (lens) & waifu2x (hd) hanya menerima JPEG/PNG. Fungsi ini
// mendekode WebP di buffer lalu meng-encode ulang jadi JPEG.
// =================================================================

// isWebP mendeteksi kontainer WebP dari header RIFF....WEBP.
func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		string(data[0:4]) == "RIFF" &&
		string(data[8:12]) == "WEBP"
}

// ToJPEGForAPI memastikan buffer gambar berupa JPEG yang diterima API luar.
//   - Non-WebP (JPEG/PNG biasa) dikembalikan apa adanya.
//   - WebP (stiker) didekode di memori, alpha diratakan ke latar putih
//     (JPEG tak mendukung transparansi), lalu di-encode ulang jadi JPEG.
//
// Semuanya berjalan di buffer — tidak ada file sementara.
func ToJPEGForAPI(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("data gambar kosong")
	}
	if !isWebP(data) {
		return data, nil
	}

	img, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		// WebP animasi tidak didukung dekoder pustaka Go.
		return nil, fmt.Errorf("stiker ini tidak didukung (kemungkinan stiker animasi); coba gambar/stiker statis")
	}

	// Ratakan transparansi ke latar putih agar area transparan tidak jadi hitam.
	b := img.Bounds()
	canvas := image.NewRGBA(b)
	draw.Draw(canvas, b, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, b, img, b.Min, draw.Over)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 92}); err != nil {
		return nil, fmt.Errorf("gagal mengonversi stiker ke JPEG: %w", err)
	}
	return out.Bytes(), nil
}
