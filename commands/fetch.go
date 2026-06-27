package commands

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

func init() {
	RegisterCommand(Command{
		Name:        "Fetch URL",
		Category:    "Owner",
		Aliases:     []string{"fetch", "get"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:fetch|get)\s+(https?:\/\/[^\s]+)\s*$`),
		Description: "Fetch HTTP request dengan Custom Headers Android (Khusus Owner)",
		Execute:     ExecuteFetch,
		Price: 0.09,
	}) // Menggunakan middleware khusus owner agar aman
}

func ExecuteFetch(ctx *ContextBot) error {
	re := regexp.MustCompile(`(?i)(https?:\/\/[^\s]+)`)
	matches := re.FindStringSubmatch(ctx.TextMessage)

	if len(matches) == 0 {
		return ctx.Reply("⚠️ URL tidak valid.\n\n📌 *Cara pakai:* `fetch https://google.com`")
	}

	targetUrl := matches[1]
	_ = ctx.React("⏳")

	req, err := http.NewRequest("GET", targetUrl, nil)
	if err != nil {
		_ = ctx.React("❌")
		return fmt.Errorf("gagal membuat request: %v", err)
	}

	// ==========================================
	// INJECT CUSTOM HEADERS (Penyamaran Chrome Mobile)
	// ==========================================
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("cache-control", "max-age=0")
	req.Header.Set("sec-ch-ua", `"Chromium";v="141", "Not?A_Brand";v="8"`)
	req.Header.Set("sec-ch-ua-mobile", "?1")
	req.Header.Set("sec-ch-ua-platform", `"Android"`)
	req.Header.Set("accept-language", "en-ID,en;q=0.9")
	req.Header.Set("upgrade-insecure-requests", "1")
	req.Header.Set("sec-fetch-site", "none")
	req.Header.Set("sec-fetch-mode", "navigate")
	req.Header.Set("sec-fetch-user", "?1")
	req.Header.Set("sec-fetch-dest", "document")
	req.Header.Set("priority", "u=0, i")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		_ = ctx.React("❌")
		return fmt.Errorf("gagal melakukan fetch ke server tujuan: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = ctx.React("❌")
		return fmt.Errorf("gagal membaca response body: %v", err)
	}

	bodyStr := string(bodyBytes)

	// Pastikan string terbaca UTF8 dengan benar
	if !utf8.ValidString(bodyStr) {
		bodyStr = strings.ToValidUTF8(bodyStr, "")
	}



	_ = ctx.React("✅")
	return ctx.Reply(bodyStr)
}