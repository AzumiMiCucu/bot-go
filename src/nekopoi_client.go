package src

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// =================================================================
// NEKOPOI NETWORK CLIENT — penembus blokir ISP (Telkomsel & sejenis)
//
// nekopoi.care di jaringan Indonesia diblokir pada DUA lapis sekaligus:
//   1. DNS transparan — query ke resolver mana pun (termasuk 1.1.1.1:53)
//      dibajak sehingga host tak me-resolve / diarahkan ke halaman blokir.
//   2. SNI/DPI — saat TLS ClientHello memuat "nekopoi.care", koneksi
//      di-RESET oleh peer (deep packet inspection).
// Ditambah masalah ke-3: sertifikat TLS situs SUDAH KADALUARSA.
//
// Solusi TANPA proxy eksternal (murni Go), diverifikasi bisa menembus:
//   • Resolusi via DNS-over-HTTPS (port 443, tak bisa diintersep ISP).
//   • Fragmentasi ClientHello (pecah tulisan TLS pertama ke beberapa
//     segmen TCP) agar DPI tak membaca string SNI utuh.
//   • InsecureSkipVerify untuk sertifikat kadaluarsa.
//
// Dipakai untuk MENGAMBIL HTML nekopoi.care MAUPUN mengunduh gambar
// poster (yang juga di-host di nekopoi.care, jadi ikut terblokir).
// =================================================================

const nekopoiHost = "nekopoi.care"

// nekoDoHEndpoints = resolver DNS-over-HTTPS (JSON API) untuk mendapatkan IP asli.
var nekoDoHEndpoints = []string{
	"https://1.1.1.1/dns-query",
	"https://dns.google/resolve",
}

// nekoFallbackIPs = IP Cloudflare cadangan bila DoH gagal total.
var nekoFallbackIPs = []string{"104.21.14.33", "172.67.157.174"}

type nekoIPCache struct {
	mu      sync.Mutex
	ips     []string
	expires time.Time
}

var nekoDNS = &nekoIPCache{}

// resolve mengembalikan daftar IP nekopoi.care (cache 10 menit), memakai DoH lalu
// jatuh ke IP cadangan.
func (c *nekoIPCache) resolve() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ips) > 0 && time.Now().Before(c.expires) {
		return c.ips
	}
	ips := nekoResolveDoH(nekopoiHost)
	if len(ips) == 0 {
		ips = append([]string{}, nekoFallbackIPs...)
	}
	c.ips = ips
	c.expires = time.Now().Add(10 * time.Minute)
	return ips
}

// nekoResolveDoH menanyakan A record via DoH JSON. Format respons Cloudflare &
// Google sama-sama { "Answer": [ { "type":1, "data":"1.2.3.4" } ] }.
func nekoResolveDoH(host string) []string {
	type answer struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	}
	var out []string
	for _, ep := range nekoDoHEndpoints {
		req, err := http.NewRequest(http.MethodGet, ep+"?type=A&name="+host, nil)
		if err != nil {
			continue
		}
		req.Header.Set("accept", "application/dns-json")
		resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		var r struct {
			Answer []answer `json:"Answer"`
		}
		if json.Unmarshal(body, &r) != nil {
			continue
		}
		for _, a := range r.Answer {
			if a.Type == 1 && net.ParseIP(a.Data) != nil {
				out = append(out, a.Data)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

// nekoSplitConn membungkus net.Conn dan memecah tulisan PERTAMA (TLS ClientHello)
// menjadi beberapa segmen TCP kecil sehingga DPI tak dapat mencocokkan SNI utuh.
type nekoSplitConn struct {
	net.Conn
	firstWriteDone bool
}

func (s *nekoSplitConn) Write(b []byte) (int, error) {
	if s.firstWriteDone || len(b) < 64 {
		return s.Conn.Write(b)
	}
	s.firstWriteDone = true
	// Pecah sangat awal: segmen pertama nyaris kosong → DPI tak melihat SNI.
	chunks := [][]byte{b[:3], b[3:24], b[24:]}
	total := 0
	for _, c := range chunks {
		n, err := s.Conn.Write(c)
		total += n
		if err != nil {
			return total, err
		}
		time.Sleep(12 * time.Millisecond)
	}
	return total, nil
}

// nekoDial menyambung ke nekopoi.care lewat IP hasil DoH, dibungkus fragmentasi.
func nekoDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	// Hanya host nekopoi yang diperlakukan khusus; host lain dial normal.
	if !strings.EqualFold(host, nekopoiHost) {
		return (&net.Dialer{Timeout: 12 * time.Second}).DialContext(ctx, network, addr)
	}
	dialer := &net.Dialer{Timeout: 12 * time.Second}
	var lastErr error
	for _, ip := range nekoDNS.resolve() {
		raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
		if err != nil {
			lastErr = err
			continue
		}
		return &nekoSplitConn{Conn: raw}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("tidak ada IP nekopoi yang bisa dijangkau")
	}
	return nil, lastErr
}

// NekoClient = http.Client khusus nekopoi.care (DoH + anti-DPI + skip cert).
var NekoClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		DialContext:         nekoDial,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		TLSHandshakeTimeout: 15 * time.Second,
		ForceAttemptHTTP2:   false,
		// Paksa HTTP/1.1 agar perilaku identik dengan yang telah diverifikasi.
		MaxIdleConns:        16,
		IdleConnTimeout:     90 * time.Second,
	},
}

// nekoUA = User-Agent browser agar tak ditolak WAF.
const nekoUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// NekoGet mengambil sebuah URL nekopoi.care dan mengembalikan body HTML mentah.
func NekoGet(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", nekoUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := NekoClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// NekoDownload mengunduh media (mis. poster) dari nekopoi.care → bytes + mime.
func NekoDownload(rawURL string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", nekoUA)
	resp, err := NekoClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", err
	}
	mime := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/jpeg"
	}
	return data, mime, nil
}
