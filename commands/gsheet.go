package commands

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"bot-go/src"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// =================================================================
// GOOGLE SHEETS (owner only) — tracker nilai/data kuliah.
//
//	nilai                              → tampilkan isi tracker
//	nilai tambah <matkul> | <sks> | <nilai>
//
// Spreadsheet "Tracker Nilai - Bot" dibuat otomatis sekali; ID disimpan di
// src/secret/nilai_sheet.txt agar dipakai ulang.
// =================================================================

const nilaiSheetIDFile = "src/secret/nilai_sheet.txt"
const nilaiSheetTitle = "Tracker Nilai - Bot"

func init() {
	RegisterCommand(Command{
		Name:        "Nilai (Sheets)",
		Category:    "Owner",
		Aliases:     []string{"nilai", "sheet"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:nilai|sheet)(?:\s+([\s\S]+))?\s*$`),
		Description: "[Owner] Tracker nilai kuliah ke Google Sheets",
		Execute:     ExecuteNilai,
	}).Use(OwnerOnlyMiddleware)
}

func sheetsService(ctx *ContextBot) (*sheets.Service, error) {
	c, err := src.GoogleClient(ctx.Ctx)
	if err != nil {
		return nil, err
	}
	return sheets.NewService(ctx.Ctx, option.WithHTTPClient(c))
}

// ensureNilaiSheet mengembalikan ID spreadsheet tracker (membuatnya bila belum ada).
func ensureNilaiSheet(ctx context.Context, srv *sheets.Service) (string, error) {
	if b, err := os.ReadFile(nilaiSheetIDFile); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	sp, err := srv.Spreadsheets.Create(&sheets.Spreadsheet{
		Properties: &sheets.SpreadsheetProperties{Title: nilaiSheetTitle},
	}).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	// Baris header.
	_, _ = srv.Spreadsheets.Values.Append(sp.SpreadsheetId, "A1", &sheets.ValueRange{
		Values: [][]interface{}{{"Mata Kuliah", "SKS", "Nilai", "Tanggal"}},
	}).ValueInputOption("RAW").Context(ctx).Do()
	_ = os.WriteFile(nilaiSheetIDFile, []byte(sp.SpreadsheetId), 0600)
	return sp.SpreadsheetId, nil
}

func ExecuteNilai(ctx *ContextBot) error {
	arg := strings.TrimSpace(ctx.Args)
	parts := strings.SplitN(arg, " ", 2)
	sub := strings.ToLower(parts[0])
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}

	switch sub {
	case "tambah", "add":
		return nilaiAdd(ctx, rest)
	default:
		return nilaiList(ctx)
	}
}

func nilaiAdd(ctx *ContextBot, rest string) error {
	if rest == "" {
		return ctx.Reply("⚠️ Format: `nilai tambah <matkul> | <sks> | <nilai>`\nContoh: `nilai tambah Fisika Dasar | 3 | A`")
	}
	cols := strings.Split(rest, "|")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	matkul := cols[0]
	if matkul == "" {
		return ctx.Reply("⚠️ Nama mata kuliah kosong.")
	}
	sks, nilai := "", ""
	if len(cols) > 1 {
		sks = cols[1]
	}
	if len(cols) > 2 {
		nilai = cols[2]
	}

	_ = ctx.React("📊")
	srv, err := sheetsService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 30*time.Second)
	defer cancel()
	id, err := ensureNilaiSheet(c, srv)
	if err != nil {
		return ctx.Reply("❌ Gagal menyiapkan spreadsheet: " + err.Error())
	}
	row := [][]interface{}{{matkul, sks, nilai, time.Now().In(jakartaLoc).Format("2006-01-02")}}
	_, err = srv.Spreadsheets.Values.Append(id, "A1", &sheets.ValueRange{Values: row}).
		ValueInputOption("RAW").Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal menambah baris: " + err.Error())
	}
	return ctx.Reply(fmt.Sprintf("✅ Dicatat: *%s* — SKS %s, Nilai %s\n🔗 https://docs.google.com/spreadsheets/d/%s/edit", matkul, sks, nilai, id))
}

func nilaiList(ctx *ContextBot) error {
	_ = ctx.React("📊")
	srv, err := sheetsService(ctx)
	if err != nil {
		return ctx.Reply("❌ " + err.Error())
	}
	c, cancel := context.WithTimeout(ctx.Ctx, 30*time.Second)
	defer cancel()
	id, err := ensureNilaiSheet(c, srv)
	if err != nil {
		return ctx.Reply("❌ Gagal menyiapkan spreadsheet: " + err.Error())
	}
	res, err := srv.Spreadsheets.Values.Get(id, "A1:D100").Context(c).Do()
	if err != nil {
		return ctx.Reply("❌ Gagal membaca data: " + err.Error())
	}
	if len(res.Values) <= 1 {
		return ctx.Reply("📭 Tracker masih kosong.\n\nTambah: `nilai tambah Fisika Dasar | 3 | A`\n🔗 https://docs.google.com/spreadsheets/d/" + id + "/edit")
	}
	var sb strings.Builder
	sb.WriteString("📊 *TRACKER NILAI*\n\n")
	for i, r := range res.Values {
		if i == 0 {
			continue // header
		}
		cell := func(n int) string {
			if n < len(r) {
				if s, ok := r[n].(string); ok {
					return s
				}
				return fmt.Sprintf("%v", r[n])
			}
			return "-"
		}
		sb.WriteString(fmt.Sprintf("%d. *%s* — %s SKS, nilai *%s*\n", i, cell(0), cell(1), cell(2)))
	}
	sb.WriteString("\n🔗 https://docs.google.com/spreadsheets/d/" + id + "/edit")
	return ctx.Reply(sb.String())
}
