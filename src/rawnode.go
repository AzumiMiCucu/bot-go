package src

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// =================================================================
// CAPTURE RAW XMPP NODE
// =================================================================
// KOREKSI asumsi lama: whatsmeow TERNYATA membuka node XMPP mentah — sama seperti
// `ws.on('CB:message')` di Baileys. Buktinya pada whatsmeow client.go:
//
//	cli.recvLog.Debugf("%s", node.XMLString())
//
// Saat level DEBUG, tiap node masuk tercetak. Lebih penting: `node.XMLString()`
// adalah ARGUMEN (dievaluasi eager oleh Go), jadi string node SUDAH dihitung tiap
// frame walau level WARN — menangkapnya nyaris gratis.
//
// whatsmeow tak menyediakan callback publik untuk node (nodeHandlers unexported),
// jadi satu-satunya seam adalah LOGGER. Kita pasang waLog.Logger kustom yang
// mencegat output sub-logger "Recv": setiap node <message ...> kita parse atribut
// & child-tag-nya lalu simpan (dikunci message-ID, TTL) agar DetectBot/cekbot bisa
// membaca sidik jari node ASLI yang sebelumnya tak terjangkau dari level protobuf.
// =================================================================

// RawNodeReport = sidik jari node XMPP <message> mentah satu pesan.
type RawNodeReport struct {
	Available bool              // true bila node berhasil ditangkap & diparse
	Attrs     map[string]string // atribut <message> tingkat-atas (from/participant/type/notify/edit/addressing_mode/category/t/...)
	ChildTags []string          // tag anak langsung (enc/bot/biz/verified_name/meta/device-identity/franking/...)
	EncVer    string            // atribut v pada <enc> (versi enkripsi)
	EncType   string            // atribut type pada <enc> (msg/pkmsg/skmsg/...)
	// EncDecryptFail = atribut decrypt-fail pada <enc>. ""/"show" = default (device
	// gagal-dekripsi tampil placeholder); "hide" = sembunyikan total. Klien WA resmi
	// hanya set "hide" utk reaction/poll-update/edit — "hide" pada pesan teks/media
	// biasa = tanda sPR (pengirim menyembunyikan pesan dari sebagian anggota). Dipakai
	// commands/antispr.go untuk deteksi sPR walau bot BUKAN target (bot tetap lihat atribut).
	EncDecryptFail string
	// EncHideAny = true bila ADA node <enc> mana pun (bukan hanya yang pertama) ber-
	// atribut decrypt-fail=hide. Penting: pesan grup sPR memuat DUA enc — konten
	// <enc type=skmsg decrypt-fail=hide> DAN <enc type=pkmsg> (SKDM kunci baru). Kalau
	// bot bukan target, whatsmeow memproses/gagal pada pkmsg (fail="") duluan & berhenti,
	// jadi DecryptFailMode event = "" walau skmsg-nya SEBENARNYA hide. Scan semua enc
	// menangkap hide itu → deteksi sPR walau bot bukan target & gagal karena sesi rusak.
	EncHideAny bool
	// HideEncType = tipe enc yang ber-hide (biasanya "skmsg"); "" bila tak ada.
	HideEncType string
	// EncTypes = semua tipe <enc> dalam node (mis. ["skmsg","pkmsg"]) — diagnostik.
	EncTypes []string
}

// Has: true bila child-tag bernama name ada.
func (r RawNodeReport) Has(name string) bool {
	for _, t := range r.ChildTags {
		if t == name {
			return true
		}
	}
	return false
}

type rawNodeEntry struct {
	report    RawNodeReport
	expiresAt time.Time
}

var (
	rawNodeCache   = make(map[string]rawNodeEntry)
	rawNodeCacheMu sync.RWMutex
)

const rawNodeTTL = 30 * time.Minute

func init() {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		for range t.C {
			now := time.Now()
			rawNodeCacheMu.Lock()
			for id, e := range rawNodeCache {
				if now.After(e.expiresAt) {
					delete(rawNodeCache, id)
				}
			}
			rawNodeCacheMu.Unlock()
		}
	}()
}

var (
	reNodeAttr = regexp.MustCompile(`([A-Za-z_][\w-]*)="([^"]*)"`)
	reChildTag = regexp.MustCompile(`<([a-z][a-z0-9_-]*)`)
	reEncBlock = regexp.MustCompile(`<enc\b([^>]*)>`)
)

// CaptureRawNode menerima XMLString sebuah node masuk dari logger kustom. Hanya
// node <message ...> yang diproses; sisanya diabaikan. Disimpan dikunci atribut id.
func CaptureRawNode(xml string) {
	if !strings.HasPrefix(xml, "<message") {
		return
	}

	// Atribut tingkat-atas <message ...> = sampai '>' pertama.
	headEnd := strings.IndexByte(xml, '>')
	head := xml
	if headEnd >= 0 {
		head = xml[:headEnd]
	}

	attrs := make(map[string]string)
	for _, m := range reNodeAttr.FindAllStringSubmatch(head, -1) {
		attrs[m[1]] = m[2]
	}
	id := attrs["id"]
	if id == "" {
		return
	}

	rep := RawNodeReport{Available: true, Attrs: attrs}

	// Child-tag langsung (unik, buang root "message").
	seen := make(map[string]bool)
	for _, m := range reChildTag.FindAllStringSubmatch(xml, -1) {
		tag := m[1]
		if tag == "message" || seen[tag] {
			continue
		}
		seen[tag] = true
		rep.ChildTags = append(rep.ChildTags, tag)
	}

	// Atribut <enc> — SCAN SEMUA blok enc (pesan grup sPR punya skmsg konten + pkmsg
	// SKDM). Enc pertama mengisi EncVer/EncType/EncDecryptFail (kompat lama); any-hide
	// diakumulasi ke EncHideAny/HideEncType (sinyal sPR walau enc pertama pkmsg biasa).
	for i, em := range reEncBlock.FindAllStringSubmatch(xml, -1) {
		var etype, edf string
		for _, a := range reNodeAttr.FindAllStringSubmatch(em[1], -1) {
			switch a[1] {
			case "v":
				if i == 0 {
					rep.EncVer = a[2]
				}
			case "type":
				etype = a[2]
				if i == 0 {
					rep.EncType = a[2]
				}
			case "decrypt-fail":
				edf = a[2]
				if i == 0 {
					rep.EncDecryptFail = a[2]
				}
			}
		}
		if etype != "" {
			rep.EncTypes = append(rep.EncTypes, etype)
		}
		if edf == "hide" {
			rep.EncHideAny = true
			if rep.HideEncType == "" {
				rep.HideEncType = etype
			}
		}
	}

	// Diagnostik: cetak saat ada enc ber-hide (sinyal sPR) — bantu kalibrasi lapangan.
	if rep.EncHideAny {
		fmt.Printf("[RAWNODE] hide-enc id=%s from=%s participant=%s type=%s encs=%v hideOn=%s\n",
			id, attrs["from"], attrs["participant"], attrs["type"], rep.EncTypes, rep.HideEncType)
	}

	rawNodeCacheMu.Lock()
	rawNodeCache[id] = rawNodeEntry{report: rep, expiresAt: time.Now().Add(rawNodeTTL)}
	rawNodeCacheMu.Unlock()
}

// LookupRawNode mengambil sidik jari node mentah untuk sebuah message-ID. (false
// bila tak tertangkap / kedaluwarsa).
func LookupRawNode(id string) (RawNodeReport, bool) {
	if id == "" {
		return RawNodeReport{}, false
	}
	rawNodeCacheMu.RLock()
	e, ok := rawNodeCache[id]
	rawNodeCacheMu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return RawNodeReport{}, false
	}
	return e.report, true
}

// =================================================================
// CUSTOM LOGGER — mencegat sub-logger "Recv"
// =================================================================

type captureLogger struct {
	base waLog.Logger
	mod  string
}

// NewCaptureLogger membungkus base logger agar tiap node <message> yang masuk lewat
// sub-logger "Recv" ditangkap CaptureRawNode. Output cetak tetap mengikuti base
// (mis. WARN di produksi), jadi tidak menambah kebisingan log.
func NewCaptureLogger(base waLog.Logger, mod string) waLog.Logger {
	return &captureLogger{base: base, mod: mod}
}

func (c *captureLogger) Sub(m string) waLog.Logger {
	return &captureLogger{base: c.base.Sub(m), mod: c.mod + "/" + m}
}

func (c *captureLogger) Debugf(msg string, args ...interface{}) {
	// whatsmeow: cli.recvLog.Debugf("%s", node.XMLString()) → cegat di sub "Recv".
	if strings.HasSuffix(c.mod, "/Recv") && msg == "%s" && len(args) == 1 {
		if s, ok := args[0].(string); ok {
			CaptureRawNode(s)
			// CaptureRawNewsletterNode(s) // NONAKTIF: membebani receive-loop (hex dump penuh tiap node)
		}
	}
	c.base.Debugf(msg, args...)
}

func (c *captureLogger) Warnf(msg string, args ...interface{})  { c.base.Warnf(msg, args...) }
func (c *captureLogger) Errorf(msg string, args ...interface{}) { c.base.Errorf(msg, args...) }
func (c *captureLogger) Infof(msg string, args ...interface{})  { c.base.Infof(msg, args...) }
