package commands

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// getSafeID tidak perlu di-deklarasi ulang di sini jika sudah ada di file lain 
// dalam package 'commands' (misalnya di handler atau registry). 
// Jika belum ada, biarkan saja. Saya menghapusnya karena biasanya ini ada di handler.go

func init() {
	RegisterCommand(Command{
		Name:        "Terminal Exec",
		Category:    "Owner",
		Aliases:     []string{"$"},
		// Menangkap pesan yang diawali dengan "$" spasi "perintah"
		Pattern:     regexp.MustCompile(`^\$\s+(.+)`),
		Description: "Mengeksekusi perintah terminal (Khusus Owner)",
		Execute:     ExecuteTerminal,
	}).Use(OwnerOnlyMiddleware) // Middleware ini sudah memblokir selain owner!
}

func ExecuteTerminal(ctx *ContextBot) error {
	// 1. Ambil argumen perintah
	cmdStr := strings.TrimSpace(ctx.Args)
	if cmdStr == "" {
		return ctx.Reply("⚠️ Format salah. Gunakan: `$ <perintah>`\nContoh: `$ ls -la`")
	}

	// 2. Eksekusi Command di sistem operasi (sebagai Bash Script)
	cmd := exec.Command("bash", "-c", cmdStr)
	
	// CombinedOutput mengambil data keberhasilan (stdout) maupun error (stderr) dari terminal
	out, err := cmd.CombinedOutput()
	outputStr := string(out)

	// 3. Pembatasan panjang karakter
/*	if len(outputStr) > 4000 {
		outputStr = outputStr[:4000] + "\n\n... [Output terlalu panjang, dipotong oleh sistem]"
	}*/

	// Jika output kosong tapi berhasil (misalnya command 'mkdir', 'touch')
	if outputStr == "" {
		outputStr = "(Command dieksekusi tanpa output teks)"
	}

	// 4. Menyusun tampilan UI balasan
	var replyText string
	if err != nil {
		replyText = fmt.Sprintf(strings.TrimSpace(outputStr))
	} else {
		replyText = fmt.Sprintf(strings.TrimSpace(outputStr))
	}

	return ctx.Reply(replyText)
}