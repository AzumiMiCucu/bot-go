// src/config.go
package src

import (
	"encoding/json"
	"fmt"
	"os"
)

// Struktur data untuk file config.json
type Configuration struct {
	BotNumber   string `json:"botNumber"`
	OwnerNumber string `json:"ownerNumber"`
	OwnerName   string `json:"ownerName"`

	// Pengaturan fitur referral broadcast otomatis
	ReferralIntervalHours int     `json:"referralIntervalHours"`
	ReferralReward        float64 `json:"referralReward"`

	// Mode bot untuk CHAT PRIBADI (japri). Scope ini TERPISAH dari mode per-grup.
	// "public" (default) = semua chat pribadi dilayani; "self" = hanya owner.
	BotMode    string `json:"botMode"`    // "public" (default) atau "self" (hanya owner) — khusus japri
	PrefixMode bool   `json:"prefixMode"` // false = no-prefix (default), true = wajib prefix
	PrefixChar string `json:"prefixChar"` // karakter prefix saat PrefixMode aktif (default ".")

	// Panggilan masuk: bila true, semua call ke bot ditolak otomatis (anti-call).
	AntiCall bool `json:"antiCall"`

	// Auto-clear chat: bila true, tiap AutoClearMinutes menit bot membersihkan
	// (menghapus dari tampilan akun bot via app-state) semua chat yang aktif —
	// kecuali chat owner. Tidak menghapus pesan untuk orang lain.
	AutoClearChat    bool `json:"autoClearChat"`
	AutoClearMinutes int  `json:"autoClearMinutes"` // default 30
}

var AppConfig *Configuration

const configFilePath = "config.json"

func InitConfig() {
	configFile := configFilePath

	// Nilai default jika file config.json belum ada
	defaultConfig := Configuration{
		BotNumber:             "573026384325",  // Nomor bot Anda (tanpa + atau 0)
		OwnerNumber:           "6281234567890", // Ganti dengan nomor owner asli Anda
		OwnerName:             "Azmi",
		ReferralIntervalHours: 12,
		ReferralReward:        0.5,
		BotMode:               "public",
		PrefixMode:            false,
		PrefixChar:            ".",
	}

	// Mengecek apakah file config.json sudah ada
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		// Jika belum ada, buat file otomatis
		data, _ := json.MarshalIndent(defaultConfig, "", "  ")
		os.WriteFile(configFile, data, 0644)
		AppConfig = &defaultConfig
		fmt.Println("[SYSTEM] File config.json otomatis dibuat. Silakan edit jika perlu.")
	} else {
		// Jika sudah ada, baca isi filenya
		data, err := os.ReadFile(configFile)
		if err != nil {
			panic("[ERROR] Gagal membaca config.json: " + err.Error())
		}

		err = json.Unmarshal(data, &AppConfig)
		if err != nil {
			panic("[ERROR] Format config.json salah/error: " + err.Error())
		}

		// Isi default untuk field baru yang mungkin belum ada di config lama
		if AppConfig.ReferralIntervalHours <= 0 {
			AppConfig.ReferralIntervalHours = 12
		}
		if AppConfig.ReferralReward <= 0 {
			AppConfig.ReferralReward = 0.5
		}
		if AppConfig.BotMode == "" {
			AppConfig.BotMode = "public"
		}
		if AppConfig.PrefixChar == "" {
			AppConfig.PrefixChar = "."
		}
		if AppConfig.AutoClearMinutes <= 0 {
			AppConfig.AutoClearMinutes = 30
		}

		fmt.Println("[SYSTEM] Konfigurasi berhasil dimuat.")
	}
}

// IsPrivateSelf mengembalikan true bila CHAT PRIBADI (japri) sedang mode SELF
// (hanya owner dilayani). DEFAULT japri = PUBLIC. Scope ini terpisah dari grup.
func IsPrivateSelf() bool {
	return AppConfig != nil && AppConfig.BotMode == "self"
}

// SetPrivateSelf mengatur mode self (true) / public (false) untuk chat pribadi
// dan langsung menyimpannya ke config.json.
func SetPrivateSelf(self bool) {
	if AppConfig == nil {
		return
	}
	if self {
		AppConfig.BotMode = "self"
	} else {
		AppConfig.BotMode = "public"
	}
	_ = SaveConfig()
}

// SaveConfig menulis kembali AppConfig ke file config.json.
// Dipakai saat owner mengubah pengaturan (mis. interval referral) secara runtime.
func SaveConfig() error {
	if AppConfig == nil {
		return fmt.Errorf("config belum diinisialisasi")
	}
	data, err := json.MarshalIndent(AppConfig, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFilePath, data, 0644)
}
