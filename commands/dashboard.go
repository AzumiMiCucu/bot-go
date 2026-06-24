package commands

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"bot-go/src"
)

func init() {
	RegisterCommand(Command{
		Name:        "Dashboard Stats",
		Category:    "Owner",
		Aliases:     []string{"dashboard", "stats"},
		Pattern:     regexp.MustCompile(`(?i)^(?:dashboard|stats|analitik)(?:\s+(.+))?`),
		Description: "Dashboard statistik bot lengkap",
		Execute:     ExecuteDashboard,
	}).Use(OwnerOnlyMiddleware) // Middleware Owner sudah langsung terpasang!
}

func ExecuteDashboard(ctx *ContextBot) error {
	subCmd := strings.ToLower(strings.TrimSpace(ctx.Args))
	switch subCmd {
	case "total", "t":
		return dashTotal(ctx)
	case "user", "users", "u":
		return dashUsers(ctx)
	case "full", "all", "f":
		return dashFull(ctx)
	case "pesan", "msg", "message", "bot", "deteksi":
		return dashMessages(ctx)
	default:
		return dashMain(ctx)
	}
}

// =============================================
// HELPER FUNCTIONS (SMART UI & TYPE SAFETY)
// =============================================

type statItem struct {
	Name  string
	Count int
}

// safeInt mencegah bot crash akibat type assertion interface{} dari SQLite
func safeInt(val interface{}) int {
	switch v := val.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func totalExec(stats map[string]int) int {
	n := 0
	for _, v := range stats {
		n += v
	}
	return n
}

// bar menghasilkan progress bar yang konsisten
func bar(val, max, width int) string {
	if max == 0 || val == 0 {
		return strings.Repeat("░", width)
	}
	filled := val * width / max
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func medalIcon(i int) string {
	switch i {
	case 0:
		return "🥇"
	case 1:
		return "🥈"
	case 2:
		return "🥉"
	default:
		return fmt.Sprintf(" %2d.", i+1)
	}
}

// extractTopCmds adalah helper untuk menghindari duplikasi kode mapping DB
func extractTopCmds(rawCmds []map[string]interface{}) []statItem {
	var list []statItem
	for _, cmd := range rawCmds {
		if cmdInfo, ok := cmd["command"]; ok {
			list = append(list, statItem{
				Name:  fmt.Sprintf("%v", cmdInfo),
				Count: safeInt(cmd["count"]),
			})
		}
	}
	return list
}

// =============================================
// MAIN DASHBOARD
// =============================================

func dashMain(ctx *ContextBot) error {
	systemStats := src.DB.GetSystemStats()
	totalStats := src.DB.GetTotalCommandStats()
	topCmds := extractTopCmds(src.DB.GetTopCommands(5))

	maxCount := 0
	if len(topCmds) > 0 {
		maxCount = topCmds[0].Count
	}

	var topStr strings.Builder
	for i, c := range topCmds {
		// Layout baru: nama di atas, bar di bawah (anti berantakan di WA)
		topStr.WriteString(fmt.Sprintf("%s *%s*\n  └─ %s (%d×)\n", medalIcon(i), c.Name, bar(c.Count, maxCount, 8), c.Count))
	}
	if topStr.Len() == 0 {
		topStr.WriteString("  _belum ada data_\n")
	}

	text := fmt.Sprintf(
		"📊 *BOT DASHBOARD*\n"+
			"_%s_\n\n"+

			"👥 *Pengguna*\n"+
			"  Terdaftar  : *%v orang*\n"+
			"  Total Saldo: *%v*\n"+
			"  Rata-rata  : *%v*\n\n"+

			"⚡ *Command*\n"+
			"  Jenis Fitur : *%d aktif*\n"+
			"  Eksekusi    : *%v kali*\n\n"+

			"🏆 *Top 5 Command*\n"+
			"%s\n"+
			"📌 Sub-command:\n"+
			"  `stats total` · `stats user` · `stats full` · `stats pesan`",
		time.Now().Format("02 Jan 2006 · 15:04 WIB"),
		systemStats["totalUsers"],
		systemStats["totalBalance"],
		systemStats["averageBalance"],
		len(totalStats),
		systemStats["totalCommands"],
		topStr.String(),
	)

	return ctx.Reply(text)
}

// =============================================
// TOTAL COMMAND STATS
// =============================================

func dashTotal(ctx *ContextBot) error {
	totalStats := src.DB.GetTotalCommandStats()
	topCmds := extractTopCmds(src.DB.GetTopCommands(20))

	if len(topCmds) == 0 {
		return ctx.Reply("📭 Belum ada statistik command.")
	}

	total := totalExec(totalStats)
	maxCount := topCmds[0].Count

	var sb strings.Builder
	sb.WriteString("📊 *STATISTIK COMMAND*\n")
	sb.WriteString(fmt.Sprintf("_%d eksekusi · %d jenis command_\n\n", total, len(totalStats)))

	for i, c := range topCmds {
		pct := 0
		if total > 0 {
			pct = c.Count * 100 / total
		}
		sb.WriteString(fmt.Sprintf("%s *%s*\n  └─ %s %d× (%d%%)\n", medalIcon(i), c.Name, bar(c.Count, maxCount, 8), c.Count, pct))
	}

	sb.WriteString(fmt.Sprintf("\n_Diperbarui %s_", time.Now().Format("02 Jan 15:04")))
	return ctx.Reply(sb.String())
}

// =============================================
// USER STATS
// =============================================

func dashUsers(ctx *ContextBot) error {
	topUsers := src.DB.GetTopUsers(10)
	systemStats := src.DB.GetSystemStats()

	if len(topUsers) == 0 {
		return ctx.Reply("📭 Belum ada pengguna terdaftar.")
	}

	var maxMsg int
	if len(topUsers) > 0 {
		maxMsg = safeInt(topUsers[0]["count"])
	}

	var sb strings.Builder
	sb.WriteString("👥 *PENGGUNA AKTIF*\n")
	sb.WriteString(fmt.Sprintf("_%v terdaftar · saldo beredar %v_\n\n", systemStats["totalUsers"], systemStats["totalBalance"]))

	for i, u := range topUsers {
		userID := fmt.Sprintf("%v", u["user"])
		count := safeInt(u["count"])

		// Ambil nomornya saja (hilangkan @s.whatsapp.net)
		displayID := strings.Split(userID, "@")[0]

		// Sensor sedikit nomor HP untuk privasi jika diperlukan (opsional, saat ini tampil penuh)
		sb.WriteString(fmt.Sprintf("%s *%s*\n  └─ %s %d cmd\n", medalIcon(i), displayID, bar(count, maxMsg, 8), count))
	}

	return ctx.Reply(sb.String())
}

// =============================================
// STATISTIK PESAN (akumulatif permanen dari msg.db)
// =============================================

func dashMessages(ctx *ContextBot) error {
	c := src.AccumCounters()
	total := c["total"]
	if total == 0 {
		return ctx.Reply("📭 Belum ada data pesan terekam. Statistik akan terisi seiring lalu lintas pesan masuk.")
	}

	media := c["media"]
	pct := func(n int) int {
		if total == 0 {
			return 0
		}
		return n * 100 / total
	}

	var sb strings.Builder
	sb.WriteString("💬 *STATISTIK PESAN*\n")
	sb.WriteString("_akumulatif sepanjang masa_\n\n")

	sb.WriteString(fmt.Sprintf("📨 Total Pesan : *%s*\n", formatRibuan(total)))
	sb.WriteString(fmt.Sprintf("🖼️ Media       : *%s* (%d%%)\n", formatRibuan(media), pct(media)))
	sb.WriteString(fmt.Sprintf("👥 Grup / Japri: *%s* / *%s*\n\n", formatRibuan(c["group"]), formatRibuan(c["private"])))

	// Breakdown verdict
	sb.WriteString("🔎 *Klasifikasi Pengirim*\n")
	for _, v := range []struct{ key, icon, label string }{
		{"verdict_human", "👤", "Manusia"},
		{"verdict_bot", "🤖", "Bot"},
		{"verdict_baileys", "🔴", "Baileys"},
		{"verdict_suspect", "🟠", "Suspect"},
		{"verdict_unknown", "⚪", "Unknown"},
	} {
		n := c[v.key]
		if n == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s %-8s %s %d%%\n", v.icon, v.label, bar(n, total, 8), pct(n)))
	}

	// Distribusi device
	sb.WriteString("\n📱 *Device*\n")
	for _, d := range []struct{ key, label string }{
		{"dev_android", "Android"}, {"dev_ios", "iOS"},
		{"dev_web", "Web"}, {"dev_desktop", "Desktop"}, {"dev_unknown", "Unknown"},
	} {
		n := c[d.key]
		if n == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("  %-8s : %s (%d%%)\n", d.label, formatRibuan(n), pct(n)))
	}

	// Top pengirim sepanjang masa
	if top := src.TopSendersAllTime(5); len(top) > 0 {
		sb.WriteString("\n🏆 *Top Pengirim*\n")
		for i, s := range top {
			sb.WriteString(fmt.Sprintf("%s %s — %s pesan\n", medalIcon(i), senderName(s), formatRibuan(s.Total)))
		}
	}

	// Top pengirim bot
	if bots := src.TopBotSendersAllTime(5); len(bots) > 0 {
		sb.WriteString("\n🚨 *Top Pengirim Bot*\n")
		for i, s := range bots {
			sb.WriteString(fmt.Sprintf("%s %s — %s deteksi\n", medalIcon(i), senderName(s), formatRibuan(s.Bot)))
		}
	}

	sb.WriteString(fmt.Sprintf("\n_Diperbarui %s_", time.Now().Format("02 Jan 15:04")))
	return ctx.Reply(sb.String())
}

// senderName menampilkan nama pengirim (pushname bila ada, jika tidak nomornya).
func senderName(s src.SenderStat) string {
	if n := strings.TrimSpace(s.PushName); n != "" {
		return n
	}
	if s.Sender == "" {
		return "Unknown"
	}
	return strings.Split(s.Sender, "@")[0]
}

// =============================================
// FULL DASHBOARD
// =============================================

func dashFull(ctx *ContextBot) error {
	systemStats := src.DB.GetSystemStats()
	totalStats := src.DB.GetTotalCommandStats()
	topCmds := extractTopCmds(src.DB.GetTopCommands(5))
	topUsers := src.DB.GetTopUsers(1)

	maxC := 0
	if len(topCmds) > 0 {
		maxC = topCmds[0].Count
	}

	var cmdSb strings.Builder
	for i, c := range topCmds {
		cmdSb.WriteString(fmt.Sprintf("%s *%s*\n  └─ %s (%d)\n", medalIcon(i), c.Name, bar(c.Count, maxC, 8), c.Count))
	}

	// Get top user
	topUserStr := "-"
	if len(topUsers) > 0 {
		userID := fmt.Sprintf("%v", topUsers[0]["user"])
		displayID := strings.Split(userID, "@")[0]
		count := safeInt(topUsers[0]["count"])
		topUserStr = fmt.Sprintf("👑 *%s*\n  └─ Eksekusi: %d kali", displayID, count)
	}

	text := fmt.Sprintf(
		"📊 *FULL ANALYTICS*\n"+
			"_%s_\n\n"+

			"👥 *User Analytics*\n"+
			"  Total Users : *%v orang*\n"+
			"  Active (24h): *%v orang*\n"+
			"  Online (1h) : *%v orang*\n"+
			"  Total Saldo : *%v*\n"+
			"  Rata-rata   : *%v*\n\n"+

			"⚡ *Command Analytics*\n"+
			"  Jenis Command : *%d command*\n"+
			"  Total Eksekusi: *%v kali*\n"+
			"  Total Transaksi: *%v*\n\n"+

			"🏆 *Top 5 Command*\n"+
			"%s\n"+
			"%s\n\n"+
			"_Diperbarui sekarang_",
		time.Now().Format("02 Jan 2006 · 15:04 WIB"),
		systemStats["totalUsers"],
		systemStats["activeUsers"],
		systemStats["onlineUsers"],
		systemStats["totalBalance"],
		systemStats["averageBalance"],
		len(totalStats),
		systemStats["totalCommands"],
		systemStats["totalTransactions"],
		cmdSb.String(),
		topUserStr,
	)

	return ctx.Reply(text)
}
