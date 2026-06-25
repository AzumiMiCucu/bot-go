package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src"
)

func init() {
	RegisterCommand(Command{
		Name:        "Group Stats",
		Category:    "Group",
		Aliases:     []string{"gstats", "grupstats"},
		Pattern:     regexp.MustCompile(`(?i)^(?:gstats|grupstats)(?:\s+(.+))?$`),
		Description: "Analitik grup (ringkasan teks)",
		Execute:     ExecuteGroupStats,
	}).Use(GroupOnlyMiddleware)
}

// Regex hanya mendeteksi Emoji, tidak menghapus Arab/Jepang/Korea.
var emojiRegex = regexp.MustCompile(`[\x{1F600}-\x{1F64F}\x{1F300}-\x{1F5FF}\x{1F680}-\x{1F6FF}\x{1F700}-\x{1F77F}\x{1F780}-\x{1F7FF}\x{1F800}-\x{1F8FF}\x{1F900}-\x{1F9FF}\x{1FA00}-\x{1FA6F}\x{1FA70}-\x{1FAFF}\x{2600}-\x{26FF}\x{2700}-\x{27BF}\x{2300}-\x{23FF}\x{2B50}\x{1F004}\x{1F0CF}\x{25AA}\x{25AB}\x{25B6}\x{25C0}\x{25FB}-\x{25FE}\x{FE0F}]`)

func cleanText(s string) string {
	return strings.TrimSpace(emojiRegex.ReplaceAllString(s, ""))
}

func ExecuteGroupStats(ctx *ContextBot) error {
	groupID := ctx.ChatJID.ToNonAD().String()
	args := strings.TrimSpace(strings.ToLower(ctx.Args))

	now := time.Now()
	targetDate := now.Format("2006-01-02")
	displayDate := now.Format("02 Jan 2006")
	isTotalMode := false
	isDailyMode := false

	if args != "" {
		switch {
		case args == "top":
			return RenderTopMemberReport(ctx, groupID, "Sepanjang Masa")
		case args == "all" || args == "total" || args == "bulan ini":
			isTotalMode = true
			displayDate = "Total Bulan " + now.Format("Jan 2006")
		case args == "yesterday":
			yesterday := now.AddDate(0, 0, -1)
			targetDate = yesterday.Format("2006-01-02")
			displayDate = yesterday.Format("02 Jan 2006")
		case regexp.MustCompile(`^\d{1,2}$`).MatchString(args):
			dayNum, _ := strconv.Atoi(args)
			if dayNum >= 1 && dayNum <= 31 {
				customDate := time.Date(now.Year(), now.Month(), dayNum, 0, 0, 0, 0, now.Location())
				targetDate = customDate.Format("2006-01-02")
				displayDate = customDate.Format("02 Jan 2006")
			}
		case regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(args):
			targetDate = args
			parsedTime, _ := time.Parse("2006-01-02", args)
			displayDate = parsedTime.Format("02 Jan 2006")
		}
	}

	// ── Info grup + admin ──
	groupName := "Unknown Group"
	memberCount := 0
	adminCount := 0
	if groupInfo, err := ctx.Client.GetGroupInfo(ctx.Ctx, ctx.ChatJID); err == nil {
		groupName = cleanText(groupInfo.Name)
		memberCount = len(groupInfo.Participants)
		for _, p := range groupInfo.Participants {
			if p.IsAdmin || p.IsSuperAdmin {
				adminCount++
			}
		}
	}

	// ── Data trafik (bot.db group_stats) ──
	totalMsg := 0
	peakVal := 0
	peakLabel := ""
	nBuckets := 0

	if isTotalMode {
		isDailyMode = true
		dataMap := make(map[int]int)
		for _, d := range src.DB.GetMonthlyGroupStats(groupID, now.Month(), now.Year()) {
			dataMap[d.Day] = d.Message
		}
		for i := 1; i <= 31; i++ {
			val := dataMap[i]
			totalMsg += val
			nBuckets++
			if val > peakVal {
				peakVal = val
				peakLabel = "Tgl " + strconv.Itoa(i)
			}
		}
	} else {
		dataMap := make(map[int]int)
		for _, d := range src.DB.GetHourlyGroupStats(groupID, targetDate) {
			dataMap[d.Hour] = d.Message
		}
		for i := 0; i <= 23; i++ {
			val := dataMap[i]
			totalMsg += val
			nBuckets++
			if val > peakVal {
				peakVal = val
				peakLabel = fmt.Sprintf("Pukul %02d:00", i)
			}
		}
	}

	// ── Data live 7 hari ──
	live := src.GroupLiveStats(groupID, 7)

	if totalMsg == 0 && live.Total == 0 {
		return ctx.Reply(fmt.Sprintf("📭 Belum ada data interaksi yang terekam untuk grup ini pada periode *%s*.", displayDate))
	}

	avgMsg := 0.0
	if nBuckets > 0 {
		avgMsg = float64(totalMsg) / float64(nBuckets)
	}
	periodUnit := "jam"
	if isDailyMode {
		periodUnit = "hari"
	}
	if peakLabel == "" {
		peakLabel = "-"
	}

	humanPct, botPct, mediaPct := pct(live.HumanMsgs, live.Total), pct(live.BotMsgs, live.Total), pct(live.Media, live.Total)
	topDev, _ := live.TopDevice()

	var sb strings.Builder
	sb.WriteString("📊 *LAPORAN ANALITIK GRUP*\n")
	sb.WriteString(fmt.Sprintf("🏢 *Grup:* %s\n", groupName))
	sb.WriteString(fmt.Sprintf("📅 *Periode:* %s\n", displayDate))
	sb.WriteString(fmt.Sprintf("👥 *Member:* %d  •  *Admin:* %d\n\n", memberCount, adminCount))

	sb.WriteString("📈 *TRAFIK*\n")
	sb.WriteString(fmt.Sprintf(" ▫️ Total interaksi : *%s pesan*\n", formatRibuan(totalMsg)))
	sb.WriteString(fmt.Sprintf(" ▫️ Rata-rata       : *%.1f* / %s\n", avgMsg, periodUnit))
	sb.WriteString(fmt.Sprintf(" ▫️ Titik teramai   : *%s* (*%s* pesan)\n\n", peakLabel, formatRibuan(peakVal)))

	sb.WriteString("🛰️ *AKTIVITAS 7 HARI*\n")
	sb.WriteString(fmt.Sprintf(" ▫️ Total pesan     : *%s*\n", formatRibuan(live.Total)))
	sb.WriteString(fmt.Sprintf(" ▫️ Pengirim unik   : *%d*\n", live.UniqueSenders))
	sb.WriteString(fmt.Sprintf(" ▫️ Manusia vs Bot  : *%s* (%d%%) / *%s* (%d%%)\n", formatRibuan(live.HumanMsgs), humanPct, formatRibuan(live.BotMsgs), botPct))
	sb.WriteString(fmt.Sprintf(" ▫️ Pesan media     : *%s* (%d%%)\n", formatRibuan(live.Media), mediaPct))
	sb.WriteString(fmt.Sprintf(" ▫️ Aktif hari ini  : *%s*\n", formatRibuan(live.ActiveToday)))
	sb.WriteString(fmt.Sprintf(" ▫️ Device dominan  : *%s*\n", strings.ToUpper(topDev)))
	sb.WriteString(fmt.Sprintf(" ▫️ Jam tersibuk    : *%s*\n", peakHourLabel(live)))

	sb.WriteString("\n_Ketik_ `gstats top` _untuk leaderboard member._")

	return ctx.Reply(sb.String())
}

// pct menghitung persentase bulat (a dari total), aman untuk total 0.
func pct(a, total int) int {
	if total <= 0 {
		return 0
	}
	return int(float64(a)/float64(total)*100 + 0.5)
}

// peakHourLabel memformat jam tersibuk live menjadi "HH:00 (N)".
func peakHourLabel(live src.GroupLiveStat) string {
	if live.PeakHour < 0 || live.PeakHourVal == 0 {
		return "-"
	}
	return fmt.Sprintf("%02d:00 (%d pesan)", live.PeakHour, live.PeakHourVal)
}

// RenderTopMemberReport menampilkan leaderboard member teraktif (teks).
func RenderTopMemberReport(ctx *ContextBot, groupID string, dateLabel string) error {
	topUsers := src.DB.GetTopGroupUsers(groupID, 10)
	if len(topUsers) == 0 {
		return ctx.Reply("📭 Belum ada data interaksi yang cukup untuk menampilkan Top Member.")
	}
	live := src.GroupLiveStats(groupID, 7)

	var sb strings.Builder
	sb.WriteString("🏆 *LEADERBOARD MEMBER TERAKTIF*\n")
	sb.WriteString(fmt.Sprintf("📅 *Periode:* %s\n\n", dateLabel))

	for i, u := range topUsers {
		name := cleanText(u.Name)
		if name == "" {
			name = strings.Split(u.UserID, "@")[0]
		}
		if len(name) > 25 {
			name = name[:23] + ".."
		}
		rank := fmt.Sprintf("*%d.*", i+1)
		switch i {
		case 0:
			rank = "🥇"
		case 1:
			rank = "🥈"
		case 2:
			rank = "🥉"
		}
		sb.WriteString(fmt.Sprintf("%s *%s*\n", rank, name))
		sb.WriteString(fmt.Sprintf("     └ %s pesan • %s media • %s kata\n", formatRibuan(u.MessageCount), formatRibuan(u.MediaCount), formatRibuan(u.WordCount)))
	}

	sb.WriteString(fmt.Sprintf("\n🛰️ *7 hari terakhir:* %s pesan • %d pengirim unik", formatRibuan(live.Total), live.UniqueSenders))

	return ctx.Reply(sb.String())
}

func formatRibuan(num int) string {
	str := strconv.Itoa(num)
	length := len(str)
	if length <= 3 {
		return str
	}
	var result []string
	for i := length; i > 0; i -= 3 {
		start := i - 3
		if start < 0 {
			start = 0
		}
		result = append([]string{str[start:i]}, result...)
	}
	return strings.Join(result, ".")
}
