package commands

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var startTime time.Time

func init() {
	startTime = time.Now()

	RegisterCommand(Command{
		Name:        "Info System",
		Category:    "General",
		Aliases:     []string{"system", "ping",},
		
		Pattern:     regexp.MustCompile(`(?i)^\s*(system|ping|halo\s+bot|test\s+bot)\s*$`),
		Description: "Mengecek status server dan ping bot",
		Execute:     ExecuteSystemPing,
	})
}



func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

func readFileSafe(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown"
	}
	return strings.TrimSpace(string(data))
}

func getOSName() string {
	if runtime.GOOS != "linux" {
		return runtime.GOOS
	}
	data := readFileSafe("/etc/os-release")
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
		}
	}
	return "Linux"
}

func getPackages() string {
	if runtime.GOOS != "linux" {
		return "N/A"
	}
	out, err := exec.Command("sh", "-c", "dpkg-query -f '.\\n' -W | wc -l").Output()
	if err == nil {
		count := strings.TrimSpace(string(out))
		if count != "" && count != "0" {
			return count + " (dpkg)"
		}
	}
	return "Unknown"
}

func getShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
	}
	parts := strings.Split(shell, "/")
	shellName := parts[len(parts)-1]
	
	out, err := exec.Command(shellName, "--version").Output()
	if err == nil {
		firstLine := strings.Split(string(out), "\n")[0]
		words := strings.Fields(firstLine)
		if len(words) > 3 {
			return shellName + " " + words[3]
		}
	}
	return shellName
}

func getLinuxUptime() string {
	data := readFileSafe("/proc/uptime")
	parts := strings.Fields(data)
	if len(parts) > 0 {
		uptimeSec, _ := strconv.ParseFloat(parts[0], 64)
		d := time.Duration(uptimeSec) * time.Second
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		return fmt.Sprintf("%d hours, %d mins", hours, mins)
	}
	return "Unknown"
}

func getCPUModel() string {
	data := readFileSafe("/proc/cpuinfo")
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "model name") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "Unknown CPU"
}

func getRamMiB() (used, total string) {
	data := readFileSafe("/proc/meminfo")
	var memTotal, memAvailable float64
	for _, line := range strings.Split(data, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			val, _ := strconv.ParseFloat(parts[1], 64)
			if parts[0] == "MemTotal:" {
				memTotal = val
			} else if parts[0] == "MemAvailable:" {
				memAvailable = val
			}
		}
	}
	
	totMiB := memTotal / 1024
	usedMiB := (memTotal - memAvailable) / 1024
	return fmt.Sprintf("%.0fMiB", usedMiB), fmt.Sprintf("%.0fMiB", totMiB)
}



func ExecuteSystemPing(ctx *ContextBot) error {
	
	text := strings.ToLower(strings.TrimSpace(ctx.TextMessage))

	
	
	
	if strings.HasPrefix(text, "system") || strings.HasPrefix(text, "info") {
		user := os.Getenv("USER")
		if user == "" {
			user = "root"
		}
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "server.io"
		}
		arch := runtime.GOARCH
		if arch == "amd64" {
			arch = "x86_64"
		}

		osName := getOSName()
		kernel := readFileSafe("/proc/sys/kernel/osrelease")
		uptime := getLinuxUptime()
		packages := getPackages()
		shell := getShell()
		cpu := getCPUModel()
		usedRam, totRam := getRamMiB()
		procUptime := formatDuration(time.Since(startTime))
		
		latency := float64(time.Since(ctx.Msg.Info.Timestamp).Microseconds()) / 1000.0

		goVersion := runtime.Version()
		numGoroutine := runtime.NumGoroutine()
		numCPU := runtime.NumCPU()
		numCgo := runtime.NumCgoCall()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		botMemory := float64(m.Alloc) / 1024 / 1024
		numGC := m.NumGC                                 
		nextGC := float64(m.NextGC) / 1024 / 1024        

		replyText := "✅ *INFORMASI SISTEM BOT*\n\n"

		replyText += fmt.Sprintf("🖥️ *SERVER (%s@%s)*\n", user, hostname)
		replyText += "-------------------\n"
		replyText += fmt.Sprintf("├ *OS:* %s %s\n", osName, arch)
		replyText += fmt.Sprintf("├ *Kernel:* %s\n", kernel)
		replyText += fmt.Sprintf("├ *Uptime:* %s\n", uptime)
		replyText += fmt.Sprintf("├ *Packages:* %s\n", packages)
		replyText += fmt.Sprintf("├ *Shell:* %s\n", shell)
		replyText += fmt.Sprintf("├ *CPU:* %s\n", cpu)
		replyText += fmt.Sprintf("└ *Ram:* %s / %s\n\n", usedRam, totRam)

		replyText += "⚙️ *GOLANG RUNTIME*\n"
		replyText += "-------------------\n"
		replyText += fmt.Sprintf("├ *Version:* %s\n", goVersion)
		replyText += fmt.Sprintf("├ *Library:* whatsmeow\n")
		replyText += fmt.Sprintf("├ *Goroutines:* %d\n", numGoroutine)
		replyText += fmt.Sprintf("├ *Logical CPUs:* %d\n", numCPU)
		replyText += fmt.Sprintf("├ *CGO Calls:* %d\n", numCgo)
		replyText += fmt.Sprintf("├ *Siklus GC:* %d Kali\n", numGC)
		replyText += fmt.Sprintf("└ *Next GC Target:* %.2fMB\n\n", nextGC)

		replyText += "📊 *OTHER*\n"
		replyText += "-------------------\n"
		replyText += fmt.Sprintf("├ *Kecepatan:* %.4f _ms_\n", latency)
		replyText += fmt.Sprintf("├ *Bot Memory:* %.2fMB\n", botMemory)
		replyText += fmt.Sprintf("├ *Process Uptime:* %s\n", procUptime)
		replyText += fmt.Sprintf("├ *Platform:* %s\n", runtime.GOOS)
		replyText += fmt.Sprintf("└ *Hostname:* %s\n", hostname)
		
		return ctx.Reply(replyText)
	}

	
	
	
	msgTime := ctx.Msg.Info.Timestamp
	currentTime := time.Now()
	latency := currentTime.Sub(msgTime)

	replyText := fmt.Sprintf(
		"🏓 *Pong!*\n\n⏱️ *Kecepatan Respon:* %d ms",
		latency.Milliseconds(),
	)

	return ctx.Reply(replyText)
}