package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// =================================================================
// COMMAND: UPDATE (owner) — tarik versi terbaru dari repo git.
//
// Menjalankan di direktori kerja bot:
//     git fetch origin && git reset --hard origin/main
//     (setara `git pull && git reset --hard origin/main`, tapi urutannya
//      di-hard-reset agar tak pernah gagal karena konflik merge lokal).
//
// Owner-only. Hasil stdout/stderr git dikirim balik sebagai balasan.
// Setelah update kode, bot perlu di-BUILD & RESTART ulang agar perubahan
// benar-benar aktif (binary yang sedang berjalan tidak berubah sendiri).
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Update",
		Category:    "Owner",
		Aliases:     []string{"update", "gitpull"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:update|gitpull)\s*$`),
		Description: "[Owner] Tarik update terbaru dari git (git pull && git reset --hard origin/main)",
		Execute:     ExecuteUpdate,
	})
}

func ExecuteUpdate(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return ctx.Reply("⛔ Hanya owner yang bisa menjalankan update.")
	}

	wd, _ := os.Getwd()
	_ = ctx.React("⏳")

	// Timeout agar tidak menggantung bila jaringan/remote lambat.
	runCtx, cancel := context.WithTimeout(ctx.Ctx, 90*time.Second)
	defer cancel()

	// Hard-reset ke origin/main: fetch dulu, lalu reset. `git pull` biasa bisa
	// gagal saat ada perubahan lokal — hard reset selalu menyamakan ke remote.
	script := "git fetch origin && git reset --hard origin/main && pm2 restart air"
	cmd := exec.CommandContext(runCtx, "bash", "-lc", script)
	cmd.Dir = wd

	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if result == "" {
		result = "(tanpa keluaran)"
	}

	if err != nil {
		_ = ctx.React("❌")
		return ctx.Reply(fmt.Sprintf("❌ *UPDATE GAGAL*\n\n```\n%s\n```\n\nError: %v", truncateOutput(result), err))
	}

	_ = ctx.React("✅")
	return ctx.Reply(fmt.Sprintf(
		"✅ *UPDATE SELESAI*\n\n```\n%s\n```\n\n_Kode sudah disamakan dengan `origin/main`. Build & restart bot agar perubahan aktif._",
		truncateOutput(result),
	))
}

// truncateOutput menjaga balasan tetap di bawah batas panjang pesan WA.
func truncateOutput(s string) string {
	const max = 3000
	if len(s) <= max {
		return s
	}
	return "…(dipotong)…\n" + s[len(s)-max:]
}
