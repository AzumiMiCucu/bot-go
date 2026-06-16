package commands

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"bot-go/src"
)

func init() {
	RegisterCommand(Command{
		Name:        "Debug Inspector",
		Category:    "Owner",
		Aliases:     []string{">>"},
		Pattern:     regexp.MustCompile(`(?i)^>>\s+(.+)`),
		Description: "Mengintip isi variabel bot mendalam (Khusus Owner)",
		Execute:     ExecuteDebug,
	}).Use(OwnerOnlyMiddleware)
}

// Fungsi Rekursif untuk membuang tipe "Function" yang bikin Golang Crash saat JSON Marshal
func sanitizeData(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		cleanMap := make(map[string]interface{})
		for key, val := range v {
			// Jika tipenya bukan fungsi, simpan dan periksa isinya lagi
			if fmt.Sprintf("%T", val) != "func(string) error" && fmt.Sprintf("%T", val) != "func(float64) float64" && fmt.Sprintf("%T", val) != "func(float64) (bool, float64)" {
				cleanMap[key] = sanitizeData(val)
			} else {
				cleanMap[key] = "[Function Hidden]" // Tandai bahwa ini adalah fungsi
			}
		}
		return cleanMap
	case []interface{}:
		var cleanSlice []interface{}
		for _, val := range v {
			cleanSlice = append(cleanSlice, sanitizeData(val))
		}
		return cleanSlice
	default:
		return v
	}
}

func ExecuteDebug(ctx *ContextBot) error {

	// 2. Ambil argumen dan pisahkan berdasarkan titik (.)
	targetPath := strings.TrimSpace(ctx.Args)
	parts := strings.Split(targetPath, ".")
	rootVar := strings.ToLower(parts[0])


	var rootData interface{}

	// 3. Daftarkan ROOT variabel
	switch rootVar {
	case "ctx":
		rootData = ctx // Sekarang aman untuk meng-assign ini secara langsung!
	case "msg":
		rootData = ctx.Msg
	case "config":
		rootData = src.AppConfig
	case "client":
		rootData = map[string]interface{}{
			"StoreID": ctx.Client.Store.ID,
		}
	default:
		return ctx.Reply(fmt.Sprintf("⚠️ Variabel root `%s` tidak ditemukan.\n*Pilihan:* ctx, msg, config, client", rootVar))
	}

	// Trik Golang: Ubah struct ke JSON bytes DENGAN MENGABAIKAN ERROR FUNGSI
	// Karena json.Marshal standar akan crash, kita buat struct sementara atau biarkan dia membaca tipe dasarnya
	jsonBytes, err := json.Marshal(rootData)
	if err != nil && strings.Contains(err.Error(), "unsupported type") {
		// Jika masih crash karena fungsi (biasanya terjadi karena ctx langsung di-marshal),
		// kita harus menggunakan format string (fmt.Sprintf) sebagai perantara kasarnya
		
		// Tapi cara di atas terlalu jelek. Maka jalan terbaik adalah membuat copy map kasar:
		if rootVar == "ctx" {
			rootData = map[string]interface{}{
				"Client":      "[Client Object]",
				"Msg":         ctx.Msg,
				"ChatJID":     ctx.ChatJID.String(),
				"User": ctx.User,
				"SenderJID":   ctx.SenderJID.String(),
				"PushName":    ctx.PushName,
				"TextMessage": ctx.TextMessage,
				"Args":        ctx.Args,
				"UserBalance": ctx.UserBalance,
				"IsOwner":     ctx.IsOwner,
				"Reply":       "[Function]",
			}
			jsonBytes, _ = json.Marshal(rootData)
		}
	}

	var dynamicMap interface{}
	json.Unmarshal(jsonBytes, &dynamicMap)

	// Bersihkan data dari sisa-sisa fungsi yang mungkin terselip (Sanitization)
	dynamicMap = sanitizeData(dynamicMap)

	// 4. Jika user meminta data spesifik dengan titik (dot notation)
	var finalData interface{} = dynamicMap

	if len(parts) > 1 {
		current := dynamicMap
		for i := 1; i < len(parts); i++ {
			key := parts[i]

			if m, ok := current.(map[string]interface{}); ok {
				found := false
				for k, v := range m {
					if strings.EqualFold(k, key) {
						current = v
						found = true
						break
					}
				}
				if !found {
					return ctx.Reply(fmt.Sprintf("⚠️ Properti `%s` tidak ditemukan pada `%s`", key, strings.Join(parts[:i], ".")))
				}
			} else if arr, ok := current.([]interface{}); ok {
				idx, err := strconv.Atoi(key)
				if err == nil && idx >= 0 && idx < len(arr) {
					current = arr[idx]
				} else {
					return ctx.Reply(fmt.Sprintf("⚠️ Indeks `%s` tidak valid untuk Array `%s`", key, strings.Join(parts[:i], ".")))
				}
			} else {
				return ctx.Reply(fmt.Sprintf("⚠️ Tidak bisa menggali lebih dalam di properti `%s`", strings.Join(parts[:i], ".")))
			}
		}
		finalData = current
	}

	// 5. Ubah data final ke JSON yang rapi
	jsonData, err := json.MarshalIndent(finalData, "", "  ")
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Gagal membaca memori: %v", err))
	}

	// 6. Potong jika kepanjangan
	outputStr := string(jsonData)
/*	if len(outputStr) > 4000 {
		outputStr = outputStr[:4000] + "\n\n... [Terlalu panjang]"
	}*/

	if !strings.HasPrefix(outputStr, "{") && !strings.HasPrefix(outputStr, "[") {
		outputStr = strings.Trim(outputStr, "\"")
	}

	// 7. Kirim hasilnya
	replyText := fmt.Sprintf("%s", outputStr)
	return ctx.Reply(replyText)
}