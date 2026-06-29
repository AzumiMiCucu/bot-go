// src/calleraudio.go
//
// Penyiapan audio untuk PANGGILAN (playcall/call) TANPA memodifikasi library
// meowcaller. Tujuannya menghilangkan suara "kresek-kresek" sekaligus membuat
// pemutaran lebih RINGAN saat panggilan berlangsung.
//
// Kenapa kresek pada pendekatan bawaan:
//  1. Decode + resample dilakukan SINKRON di dalam ticker 60 ms milik engine.
//     Bila satu frame telat di-decode → underrun → bunyi putus/kresek.
//  2. Resampler bawaan memakai interpolasi linear TANPA low-pass anti-alias,
//     sehingga frekuensi >8 kHz "melipat" jadi artefak kasar.
//  3. MP3 mastering keras membuat sampel mendekati ±1.0 → codec MLOW (suara
//     16 kHz) overload → kresek.
//
// Solusi di sini (semua via interface AudioSource yang sudah disediakan library):
//   - PRE-DECODE seluruh file ke memori SEKALI di awal → ReadFrame jadi O(1)
//     (sekadar ambil frame berikutnya), tak ada decode di jalur real-time →
//     underrun hilang & beban per-frame nyaris nol (lebih cepat).
//   - Untuk MP3: resample sendiri dengan low-pass anti-alias (biquad Butterworth
//     orde-4) SEBELUM turun ke 16 kHz → aliasing hilang.
//   - Normalisasi puncak + soft-limiter → cegah overload MLOW.
//   - High-pass lembut → buang rumble/DC yang memboroskan codec.
package src

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	gomp3 "github.com/hajimehoshi/go-mp3"
	"github.com/purpshell/meowcaller"
)

const (
	callRate   = meowcaller.SampleRate   // 16000 Hz mono (format codec MLOW)
	callFrameN = meowcaller.FrameSamples // 960 sampel / frame (60 ms)
)

// memSource adalah AudioSource yang seluruh framenya sudah ada di memori.
// ReadFrame hanya mengembalikan frame berikutnya — tanpa decode/resample apa pun
// di jalur real-time, sehingga ticker 60 ms engine tak pernah menunggu.
type memSource struct {
	frames [][]float32
	pos    int
}

func (m *memSource) ReadFrame() ([]float32, error) {
	if m.pos >= len(m.frames) {
		return nil, io.EOF
	}
	f := m.frames[m.pos]
	m.pos++
	return f, nil
}

func (m *memSource) Close() error { return nil }

// quickValidateCallAudio memeriksa file secara MURAH (ada + ekstensi didukung)
// tanpa men-decode, agar StartCall bisa gagal-cepat sebelum menelepon namun
// decode berat tetap bisa dijalankan asinkron saat berdering.
func quickValidateCallAudio(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp3", ".wav", ".opus", ".ogg":
	default:
		return errUnsupportedAudio
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return nil
}

// errUnsupportedAudio dipakai bila ekstensi tak didukung.
var errUnsupportedAudio = &audioErr{"format audio tidak didukung (pakai .mp3/.wav/.opus)"}

type audioErr struct{ s string }

func (e *audioErr) Error() string { return e.s }

// prepareCallAudio men-decode file menjadi PCM 16 kHz mono yang sudah diolah,
// lalu memotongnya menjadi frame siap-putar di memori. Inilah pengganti
// meowcaller.MP3File/WAVFile/OpusFile yang dipakai di openAudio.
func prepareCallAudio(path string) (meowcaller.AudioSource, error) {
	var samples []float32
	var err error

	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		// Jalur utama playcall: decode + resample anti-alias sendiri.
		samples, err = decodeMP3Mono16k(path)
	case ".wav", ".opus", ".ogg":
		// Pakai decoder library untuk format ini, lalu kuras ke memori.
		var dec meowcaller.AudioSource
		if strings.HasSuffix(strings.ToLower(path), ".wav") {
			dec, err = meowcaller.WAVFile(path)
		} else {
			dec, err = meowcaller.OpusFile(path)
		}
		if err == nil {
			samples, err = drainSource(dec)
			_ = dec.Close()
		}
	default:
		return nil, errUnsupportedAudio
	}
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, &audioErr{"audio kosong setelah decode"}
	}

	// Pengolahan kualitas: high-pass rumble → normalisasi → soft-limiter.
	cleanCallSamples(samples)

	// Potong jadi frame 960 sampel (frame terakhir di-zero-pad).
	frames := make([][]float32, 0, len(samples)/callFrameN+1)
	for i := 0; i < len(samples); i += callFrameN {
		end := i + callFrameN
		frame := make([]float32, callFrameN)
		if end > len(samples) {
			copy(frame, samples[i:])
		} else {
			copy(frame, samples[i:end])
		}
		frames = append(frames, frame)
	}
	return &memSource{frames: frames}, nil
}

// drainSource membaca seluruh frame dari sebuah AudioSource library ke satu slice.
func drainSource(src meowcaller.AudioSource) ([]float32, error) {
	var all []float32
	for {
		f, err := src.ReadFrame()
		if len(f) > 0 {
			all = append(all, f...)
		}
		if err == io.EOF {
			return all, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// decodeMP3Mono16k men-decode seluruh MP3 → mono float32 pada laju aslinya,
// menerapkan low-pass anti-alias, lalu menurunkannya ke 16 kHz.
func decodeMP3Mono16k(path string) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec, err := gomp3.NewDecoder(f)
	if err != nil {
		return nil, err
	}
	inRate := dec.SampleRate()

	// go-mp3 selalu memberi s16le stereo (2 kanal). Baca semua lalu downmix.
	raw, err := io.ReadAll(dec)
	if err != nil {
		return nil, err
	}
	n := len(raw) / 4 // 2 kanal * 2 byte
	mono := make([]float32, n)
	for i := 0; i < n; i++ {
		l := int16(binary.LittleEndian.Uint16(raw[i*4:]))
		r := int16(binary.LittleEndian.Uint16(raw[i*4+2:]))
		mono[i] = (float32(l) + float32(r)) / (2 * 32768.0)
	}
	return resampleMono16k(mono, inRate), nil
}

// resampleMono16k menurunkan sinyal mono dari inRate ke 16 kHz. Bila inRate lebih
// tinggi (mis. 44100/48000), sinyal di-LOW-PASS dulu di bawah Nyquist 16 kHz
// (anti-alias) sebelum interpolasi linear, sehingga tak ada artefak "kresek".
func resampleMono16k(in []float32, inRate int) []float32 {
	if inRate <= 0 || len(in) == 0 {
		return in
	}
	if inRate == callRate {
		return in
	}

	work := in
	if inRate > callRate {
		// Cutoff 7.2 kHz: di bawah Nyquist (8 kHz) & sekitar pita berguna MLOW.
		// Butterworth orde-4 (dua biquad) untuk rolloff yang cukup tajam.
		work = make([]float32, len(in))
		copy(work, in)
		applyLowpass(work, inRate, 7200, 2)
	}

	ratio := float64(callRate) / float64(inRate)
	outN := int(float64(len(work)) * ratio)
	if outN <= 0 {
		return nil
	}
	out := make([]float32, outN)
	step := float64(inRate) / float64(callRate)
	pos := 0.0
	for i := 0; i < outN; i++ {
		idx := int(pos)
		if idx+1 < len(work) {
			frac := float32(pos - float64(idx))
			out[i] = work[idx]*(1-frac) + work[idx+1]*frac
		} else if idx < len(work) {
			out[i] = work[idx]
		}
		pos += step
	}
	return out
}

// cleanCallSamples mengolah sinyal 16 kHz di tempat: buang DC/rumble (high-pass
// ~90 Hz), normalisasi puncak, lalu soft-limit agar tak meng-overload MLOW.
func cleanCallSamples(s []float32) {
	if len(s) == 0 {
		return
	}

	// 1. High-pass satu-kutub ~90 Hz (hilangkan DC & gemuruh sub-bass).
	hpAlpha := float32(0.985) // ~ rc untuk ~90 Hz @ 16 kHz
	var prevIn, prevOut float32
	for i, x := range s {
		y := hpAlpha * (prevOut + x - prevIn)
		prevIn = x
		prevOut = y
		s[i] = y
	}

	// 2. Normalisasi puncak ke 0.9 (level konsisten masuk codec; cegah terlalu pelan).
	var peak float32
	for _, x := range s {
		a := x
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak > 1e-6 {
		gain := float32(0.9) / peak
		if gain > 12 {
			gain = 12 // batasi penguatan agar bagian senyap tak meledak jadi noise
		}
		for i := range s {
			s[i] *= gain
		}
	}

	// 3. Soft-limiter (tanh) sebagai jaring pengaman terhadap puncak sisa.
	for i, x := range s {
		if x > 0.95 || x < -0.95 {
			s[i] = float32(math.Tanh(float64(x)))
		}
	}
}

// applyLowpass menerapkan low-pass Butterworth orde-(2*stages) ke sinyal di
// tempat, sebagai kaskade biquad. Dipakai sebagai filter anti-alias sebelum
// penurunan laju cuplik.
func applyLowpass(s []float32, rate, cutoff, stages int) {
	if rate <= 0 || cutoff <= 0 || cutoff*2 >= rate {
		return
	}
	b0, b1, b2, a1, a2 := lowpassBiquad(float64(cutoff), float64(rate))
	for st := 0; st < stages; st++ {
		var x1, x2, y1, y2 float64
		for i, xf := range s {
			x := float64(xf)
			y := b0*x + b1*x1 + b2*x2 - a1*y1 - a2*y2
			x2, x1 = x1, x
			y2, y1 = y1, y
			s[i] = float32(y)
		}
	}
}

// lowpassBiquad menghitung koefisien biquad low-pass (Q Butterworth ~0.707).
func lowpassBiquad(cutoff, rate float64) (b0, b1, b2, a1, a2 float64) {
	w0 := 2 * math.Pi * cutoff / rate
	cosw := math.Cos(w0)
	alpha := math.Sin(w0) / (2 * 0.70710678)
	a0 := 1 + alpha
	b0 = (1 - cosw) / 2 / a0
	b1 = (1 - cosw) / a0
	b2 = b0
	a1 = (-2 * cosw) / a0
	a2 = (1 - alpha) / a0
	return
}
