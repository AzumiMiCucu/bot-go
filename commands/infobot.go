package commands

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bot-go/src"
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

// getMemDetail mengembalikan RAM terpakai/total beserta persentasenya (jujur, dari /proc/meminfo).
func getMemDetail() (used, total string, pct int) {
	data := readFileSafe("/proc/meminfo")
	var memTotal, memAvailable float64
	for _, line := range strings.Split(data, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			val, _ := strconv.ParseFloat(parts[1], 64)
			switch parts[0] {
			case "MemTotal:":
				memTotal = val
			case "MemAvailable:":
				memAvailable = val
			}
		}
	}
	if memTotal == 0 {
		return "Unknown", "Unknown", 0
	}
	usedKiB := memTotal - memAvailable
	pct = int((usedKiB / memTotal) * 100)
	return fmt.Sprintf("%.0fMiB", usedKiB/1024), fmt.Sprintf("%.0fMiB", memTotal/1024), pct
}

// getSwap mengembalikan swap terpakai/total dari /proc/meminfo.
func getSwap() (used, total string) {
	data := readFileSafe("/proc/meminfo")
	var swapTotal, swapFree float64
	for _, line := range strings.Split(data, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			val, _ := strconv.ParseFloat(parts[1], 64)
			switch parts[0] {
			case "SwapTotal:":
				swapTotal = val
			case "SwapFree:":
				swapFree = val
			}
		}
	}
	if swapTotal == 0 {
		return "0MiB", "0MiB (tidak ada)"
	}
	return fmt.Sprintf("%.0fMiB", (swapTotal-swapFree)/1024), fmt.Sprintf("%.0fMiB", swapTotal/1024)
}

// getLoadAvg mengembalikan beban sistem 1/5/15 menit dari /proc/loadavg.
func getLoadAvg() string {
	data := readFileSafe("/proc/loadavg")
	parts := strings.Fields(data)
	if len(parts) >= 3 {
		return fmt.Sprintf("%s, %s, %s", parts[0], parts[1], parts[2])
	}
	return "Unknown"
}

// getDisk mengembalikan pemakaian disk root "/" (terpakai/total + persen), via statfs.
func getDisk() (used, total string, pct int) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return "Unknown", "Unknown", 0
	}
	bsize := float64(st.Bsize)
	totalB := float64(st.Blocks) * bsize
	freeB := float64(st.Bavail) * bsize
	usedB := totalB - freeB
	if totalB == 0 {
		return "Unknown", "Unknown", 0
	}
	pct = int((usedB / totalB) * 100)
	human := func(b float64) string {
		gb := b / (1024 * 1024 * 1024)
		if gb >= 1 {
			return fmt.Sprintf("%.1fGB", gb)
		}
		return fmt.Sprintf("%.0fMB", b/(1024*1024))
	}
	return human(usedB), human(totalB), pct
}

// getCPUCores menghitung jumlah core logis (thread) dari /proc/cpuinfo.
func getCPUCores() int {
	data := readFileSafe("/proc/cpuinfo")
	n := 0
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "processor") {
			n++
		}
	}
	if n == 0 {
		return runtime.NumCPU()
	}
	return n
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
		cores := getCPUCores()
		loadAvg := getLoadAvg()
		usedRam, totRam, ramPct := getMemDetail()
		usedSwap, totSwap := getSwap()
		usedDisk, totDisk, diskPct := getDisk()
		procUptime := formatDuration(time.Since(startTime))

		latency := float64(time.Since(ctx.Msg.Info.Timestamp).Microseconds()) / 1000.0

		goVersion := runtime.Version()
		numGoroutine := runtime.NumGoroutine()
		numCPU := runtime.NumCPU()
		maxProcs := runtime.GOMAXPROCS(0)
		numCgo := runtime.NumCgoCall()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		mb := func(b uint64) float64 { return float64(b) / 1024 / 1024 }
		heapAlloc := mb(m.HeapAlloc)
		heapInuse := mb(m.HeapInuse)
		heapIdle := mb(m.HeapIdle)
		heapReleased := mb(m.HeapReleased)
		stackInuse := mb(m.StackInuse)
		sysMem := mb(m.Sys)
		totalAlloc := mb(m.TotalAlloc)
		gcSys := mb(m.GCSys)
		nextGC := mb(m.NextGC)
		liveObjects := m.Mallocs - m.Frees
		lastGCPause := float64(m.PauseNs[(m.NumGC+255)%256]) / 1e6 // ms
		gcCPUPct := m.GCCPUFraction * 100
		lastGCAgo := "-"
		if m.LastGC > 0 {
			lastGCAgo = formatDuration(time.Since(time.Unix(0, int64(m.LastGC))))
		}

		// Statistik pesan JUJUR dari msg.db (akumulator permanen).
		stats := src.AccumCounters()
		totalMsg := stats["total"]
		botMsg := stats["verdict_bot"] + stats["verdict_baileys"]
		humanMsg := totalMsg - botMsg
		if humanMsg < 0 {
			humanMsg = 0
		}
		mediaMsg := stats["media"]
		groupMsg := stats["group"]
		privMsg := stats["private"]
		cmdCount := len(src.GetCommands())

		replyText := "✅ *INFORMASI SISTEM BOT*\n\n"

		replyText += fmt.Sprintf("🖥️ *SERVER (%s@%s)*\n", user, hostname)
		replyText += "━━━━━━━━━━━━━━━\n"
		replyText += fmt.Sprintf("├ *OS:* %s %s\n", osName, arch)
		replyText += fmt.Sprintf("├ *Kernel:* %s\n", kernel)
		replyText += fmt.Sprintf("├ *Uptime:* %s\n", uptime)
		replyText += fmt.Sprintf("├ *Packages:* %s\n", packages)
		replyText += fmt.Sprintf("├ *Shell:* %s\n", shell)
		replyText += fmt.Sprintf("├ *CPU:* %s\n", cpu)
		replyText += fmt.Sprintf("├ *Cores (thread):* %d\n", cores)
		replyText += fmt.Sprintf("├ *Load Avg:* %s\n", loadAvg)
		replyText += fmt.Sprintf("├ *RAM:* %s / %s (%d%%)\n", usedRam, totRam, ramPct)
		replyText += fmt.Sprintf("├ *Swap:* %s / %s\n", usedSwap, totSwap)
		replyText += fmt.Sprintf("└ *Disk /:* %s / %s (%d%%)\n\n", usedDisk, totDisk, diskPct)

		replyText += "⚙️ *GOLANG RUNTIME*\n"
		replyText += "━━━━━━━━━━━━━━━\n"
		replyText += fmt.Sprintf("├ *Version:* %s\n", goVersion)
		replyText += fmt.Sprintf("├ *Library:* whatsmeow\n")
		replyText += fmt.Sprintf("├ *Goroutines:* %d\n", numGoroutine)
		replyText += fmt.Sprintf("├ *Logical CPU / GOMAXPROCS:* %d / %d\n", numCPU, maxProcs)
		replyText += fmt.Sprintf("├ *CGO Calls:* %d\n", numCgo)
		replyText += fmt.Sprintf("├ *Siklus GC:* %d kali (%.2f%% CPU)\n", m.NumGC, gcCPUPct)
		replyText += fmt.Sprintf("├ *Jeda GC Terakhir:* %.3f ms\n", lastGCPause)
		replyText += fmt.Sprintf("└ *Next GC Target:* %.2f MB\n\n", nextGC)

		replyText += "🧠 *MEMORI PROSES BOT*\n"
		replyText += "━━━━━━━━━━━━━━━\n"
		replyText += fmt.Sprintf("├ *Heap Terpakai:* %.2f MB\n", heapAlloc)
		replyText += fmt.Sprintf("├ *Heap In-Use:* %.2f MB\n", heapInuse)
		replyText += fmt.Sprintf("├ *Heap Idle:* %.2f MB\n", heapIdle)
		replyText += fmt.Sprintf("├ *Dikembalikan ke OS:* %.2f MB\n", heapReleased)
		replyText += fmt.Sprintf("├ *Stack In-Use:* %.2f MB\n", stackInuse)
		replyText += fmt.Sprintf("├ *Objek Hidup:* %s objek\n", formatRibuan(int(liveObjects)))
		replyText += fmt.Sprintf("├ *Total Mallocs:* %s\n", formatRibuan(int(m.Mallocs)))
		replyText += fmt.Sprintf("├ *Memori GC (overhead):* %.2f MB\n", gcSys)
		replyText += fmt.Sprintf("├ *Reserved dari OS (Sys):* %.2f MB\n", sysMem)
		replyText += fmt.Sprintf("├ *GC Terakhir:* %s lalu\n", lastGCAgo)
		replyText += fmt.Sprintf("└ *Total Dialokasikan (akumulatif):* %.2f MB\n\n", totalAlloc)

		replyText += "📊 *STATISTIK BOT (msg.db)*\n"
		replyText += "━━━━━━━━━━━━━━━\n"
		replyText += fmt.Sprintf("├ *Command Terdaftar:* %d\n", cmdCount)
		replyText += fmt.Sprintf("├ *Total Pesan Diproses:* %s\n", formatRibuan(totalMsg))
		replyText += fmt.Sprintf("├ *Manusia / Bot:* %s / %s\n", formatRibuan(humanMsg), formatRibuan(botMsg))
		replyText += fmt.Sprintf("├ *Grup / Pribadi:* %s / %s\n", formatRibuan(groupMsg), formatRibuan(privMsg))
		replyText += fmt.Sprintf("└ *Pesan Media:* %s\n\n", formatRibuan(mediaMsg))

		replyText += "📡 *RESPON & PROSES*\n"
		replyText += "━━━━━━━━━━━━━━━\n"
		replyText += fmt.Sprintf("├ *Latensi Pesan:* %.4f ms\n", latency)
		replyText += fmt.Sprintf("├ *Process Uptime:* %s\n", procUptime)
		replyText += fmt.Sprintf("└ *Platform:* %s/%s\n", runtime.GOOS, runtime.GOARCH)

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