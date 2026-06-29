package commands

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

// =================================================================
// EVAL GO (>>) — interpreter Go runtime ala eval Node.js.
// Owner bisa memanggil SELURUH fungsi/builder/tipe paket `src` secara live,
// plus variabel runtime (Ctx, Client). Tabel simbol `src` di-GENERATE otomatis
// (file bot-go-src.go) — tambah fungsi baru di src lalu jalankan
// `go generate ./commands/`, langsung tersedia TANPA deklarasi manual.
//
//   >> 1+2
//   >> Ctx.PushName
//   >> Ctx.Reply("halo dari eval")
//   >> src.NewButton().SetBody("tes").SendToChat(Ctx)
//   >> src.NewAIRich().SetTitle("Hai").AddText("isi").SendToChat(Ctx)
//   >> src.YtMp3("https://...")
//   >> Config.OwnerName        // alias dari src.AppConfig
//   >> DB.GetUser("628xx@s.whatsapp.net")
// ( `$` tetap untuk shell command, terpisah dari ini. )
// =================================================================

// Symbols menampung tabel simbol yang disuntik ke interpreter. File ter-generate
// (bot-go-src.go) mengisinya lewat init() — JANGAN diisi manual.
//
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract -name commands bot-go/src
var Symbols = interp.Exports{}

func init() {
	RegisterCommand(Command{
		Name:        "Eval Go",
		Category:    "Owner",
		Aliases:     []string{">>"},
		Pattern:     regexp.MustCompile(`(?s)^>>\s+(.+)`),
		Description: "Evaluasi kode Go live: panggil variabel & fungsi (Owner)",
		Execute:     ExecuteEval,
	}).Use(OwnerOnlyMiddleware)
}

func ExecuteEval(ctx *ContextBot) error {
	code := strings.TrimSpace(ctx.Args)
	if code == "" {
		return ctx.Reply("⚠️ Format: `>> <kode go>`\n\nContoh:\n• `>> 1+2`\n• `>> Ctx.PushName`\n• `>> Ctx.Reply(\"halo\")`\n• `>> Config.OwnerName`\n• `>> DB.GetUser(\"628xx@s.whatsapp.net\")`\n\n_Tersedia: Ctx, Client, DB, Config_")
	}

	i := interp.New(interp.Options{Unrestricted: true})
	if err := i.Use(stdlib.Symbols); err != nil {
		return ctx.Reply("❌ Gagal init stdlib: " + err.Error())
	}

	// Suntik SELURUH simbol paket `src` (ter-generate otomatis) → semua
	// fungsi/builder/tipe `src.*` bisa dipanggil langsung di eval.
	if err := i.Use(Symbols); err != nil {
		return ctx.Reply("❌ Gagal inject simbol src: " + err.Error())
	}

	// Suntik variabel RUNTIME (nilai per-panggilan, bukan simbol paket) via "bot".
	if err := i.Use(interp.Exports{
		"bot/bot": {
			"Ctx":    reflect.ValueOf(ctx),
			"Client": reflect.ValueOf(ctx.Client),
		},
	}); err != nil {
		return ctx.Reply("❌ Gagal inject simbol: " + err.Error())
	}

	// Pre-import + alias global agar bisa langsung (src.*, Ctx, DB, Config) — best effort.
	_, _ = i.Eval(`import "bot-go/src"`)
	_, _ = i.Eval(`import "bot"`)
	_, _ = i.Eval(`var Ctx = bot.Ctx`)
	_, _ = i.Eval(`var Client = bot.Client`)
	_, _ = i.Eval(`var DB = src.DB`)
	_, _ = i.Eval(`var Config = src.AppConfig`)

	v, err := i.Eval(code)
	if err != nil {
		return ctx.Reply(err.Error())
	}

	return ctx.Reply(formatEvalResult(v))
}

func formatEvalResult(v reflect.Value) string {
	if !v.IsValid() {
		return "(OK — tidak ada nilai kembalian)"
	}
	clean := cleanValue(v, 0)
	switch clean.(type) {
	case nil:
		return "nil"
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return fmt.Sprintf("%v", clean)
	}
	if b, e := json.MarshalIndent(clean, "", "  "); e == nil {
		return string(b)
	}
	return fmt.Sprintf("%+v", clean)
}

// cleanValue mengubah nilai apa pun menjadi struktur JSON-able:
// fungsi/channel dibuang, tipe ber-String() (JID, time, dll) jadi string,
// dengan batas kedalaman agar aman dari struktur dalam/siklik.
func cleanValue(rv reflect.Value, depth int) interface{} {
	if !rv.IsValid() {
		return nil
	}
	if depth > 6 {
		return "…"
	}
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return cleanValue(rv.Elem(), depth)
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return "[func]"
	case reflect.Struct:
		if rv.CanInterface() {
			if s, ok := rv.Interface().(fmt.Stringer); ok {
				return s.String()
			}
		}
		m := map[string]interface{}{}
		t := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			if t.Field(i).PkgPath != "" { // skip unexported
				continue
			}
			cv := cleanValue(rv.Field(i), depth+1)
			if cv == nil { // jangan tampilkan field bernilai null/nil
				continue
			}
			m[t.Field(i).Name] = cv
		}
		return m
	case reflect.Map:
		m := map[string]interface{}{}
		for _, k := range rv.MapKeys() {
			cv := cleanValue(rv.MapIndex(k), depth+1)
			if cv == nil {
				continue
			}
			m[fmt.Sprint(k.Interface())] = cv
		}
		return m
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		arr := make([]interface{}, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			arr = append(arr, cleanValue(rv.Index(i), depth+1))
		}
		return arr
	default:
		if rv.CanInterface() {
			return rv.Interface()
		}
		return fmt.Sprintf("%v", rv)
	}
}

func truncate(s string, max int) string {
	s = "\n" + s + "\n"
	if len(s) > max {
		return s[:max] + "\n…(dipotong)"
	}
	return s
}
