package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"bot-go/src"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// GROUP STATS (gstats) — analitik grup DETAIL.
// Subperintah:
//   gstats                 → dashboard ringkas (hari ini)
//   gstats <tanggal>       → dashboard tanggal tsb (yesterday | 1-31 | YYYY-MM-DD | total)
//   gstats online          → siapa yang ONLINE sekarang + last-seen (data langsung WA)
//   gstats top [N]         → leaderboard member teraktif
//   gstats jam [tanggal]   → heatmap pesan per jam (bar)
//   gstats jenis [tanggal] → breakdown jenis pesan (teks/gambar/video/suara/dll)
//   gstats @user / reply   → kartu detail satu member
//   gstats help            → bantuan
// =================================================================

func init() {
	RegisterCommand(Command{
		Name:        "Group Stats",
		Category:    "Group",
		Aliases:     []string{"gstats", "grupstats"},
		Pattern:     regexp.MustCompile(`(?i)^(?:gstats|grupstats)(?:\s+([\s\S]+))?$`),
		Description: "Analitik grup detail: trafik, jenis pesan, online member, leaderboard",
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
	rawArgs := strings.TrimSpace(ctx.Args)
	args := strings.ToLower(rawArgs)

	// ── Per-member: bila ada mention atau reply ke pesan member ──
	if target, ok := resolveStatsTarget(ctx, args); ok {
		return RenderMemberDetail(ctx, groupID, target)
	}

	// ── Dispatch subperintah ──
	fields := strings.Fields(args)
	sub := ""
	rest := ""
	if len(fields) > 0 {
		sub = fields[0]
		rest = strings.TrimSpace(strings.TrimPrefix(args, sub))
	}

	switch sub {
	case "help", "?", "bantuan":
		return ctx.Reply(gstatsHelp())
	case "online", "on", "aktif":
		return RenderOnlineReport(ctx, groupID)
	case "top", "rank", "ranking", "leaderboard", "lb":
		return RenderTopMemberReport(ctx, groupID, "Sepanjang Masa", rest)
	case "jam", "hour", "jaman", "heatmap":
		date, label := parseDateArg(rest)
		return RenderHourly(ctx, groupID, date, label)
	case "jenis", "tipe", "kind", "media", "type":
		dateLike, label := parseDateLike(rest)
		return RenderKinds(ctx, groupID, dateLike, label)
	}

	// ── Default: dashboard (mendukung argumen tanggal lama) ──
	return RenderDashboard(ctx, groupID, args)
}

// =================================================================
// DASHBOARD
// =================================================================

func RenderDashboard(ctx *ContextBot, groupID, args string) error {
	now := time.Now()
	targetDate := now.Format("2006-01-02")
	displayDate := now.Format("02 Jan 2006")
	dateLike := targetDate
	isTotalMode := false
	isDailyMode := false

	switch {
	case args == "":
		// hari ini (default)
	case args == "all" || args == "total" || args == "bulan ini":
		isTotalMode = true
		displayDate = "Total Bulan " + now.Format("Jan 2006")
		dateLike = now.Format("2006-01") + "-%"
	case args == "yesterday" || args == "kemarin":
		y := now.AddDate(0, 0, -1)
		targetDate = y.Format("2006-01-02")
		displayDate = y.Format("02 Jan 2006")
		dateLike = targetDate
	case regexp.MustCompile(`^\d{1,2}$`).MatchString(args):
		if d, _ := strconv.Atoi(args); d >= 1 && d <= 31 {
			c := time.Date(now.Year(), now.Month(), d, 0, 0, 0, 0, now.Location())
			targetDate = c.Format("2006-01-02")
			displayDate = c.Format("02 Jan 2006")
			dateLike = targetDate
		}
	case regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(args):
		targetDate = args
		if p, err := time.Parse("2006-01-02", args); err == nil {
			displayDate = p.Format("02 Jan 2006")
		}
		dateLike = targetDate
	default:
		return ctx.Reply("⚠️ Argumen tidak dikenal. Ketik `gstats help` untuk daftar perintah.")
	}

	// ── Info grup + admin + presence online ──
	groupName := "Unknown Group"
	memberCount, adminCount, onlineNow := 0, 0, 0
	if gi, err := ctx.Client.GetGroupInfo(ctx.Ctx, ctx.ChatJID); err == nil {
		groupName = cleanText(gi.Name)
		memberCount = len(gi.Participants)
		for _, p := range gi.Participants {
			if p.IsAdmin || p.IsSuperAdmin {
				adminCount++
			}
			if info := participantPresence(p); info.Known && info.Online {
				onlineNow++
			}
		}
	}

	// ── Trafik (bot.db group_stats_daily) ──
	totalMsg, peakVal, peakLabel, nBuckets := 0, 0, "", 0
	if isTotalMode {
		isDailyMode = true
		m := map[int]int{}
		for _, d := range src.DB.GetMonthlyGroupStats(groupID, now.Month(), now.Year()) {
			m[d.Day] = d.Message
		}
		for i := 1; i <= 31; i++ {
			v := m[i]
			totalMsg += v
			nBuckets++
			if v > peakVal {
				peakVal, peakLabel = v, "Tgl "+strconv.Itoa(i)
			}
		}
	} else {
		m := map[int]int{}
		for _, d := range src.DB.GetHourlyGroupStats(groupID, targetDate) {
			m[d.Hour] = d.Message
		}
		for i := 0; i <= 23; i++ {
			v := m[i]
			totalMsg += v
			nBuckets++
			if v > peakVal {
				peakVal, peakLabel = v, fmt.Sprintf("Pukul %02d:00", i)
			}
		}
	}

	live := src.GroupLiveStats(groupID, 7)
	kinds := src.DB.GetGroupKindBreakdown(groupID, dateLike)

	if totalMsg == 0 && live.Total == 0 && len(kinds) == 0 {
		return ctx.Reply(fmt.Sprintf("📭 Belum ada data interaksi terekam untuk grup ini pada periode *%s*.", displayDate))
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
	sb.WriteString(fmt.Sprintf("👥 *Member:* %d  •  *Admin:* %d  •  🟢 *Online:* %d\n\n", memberCount, adminCount, onlineNow))

	sb.WriteString("📈 *TRAFIK*\n")
	sb.WriteString(fmt.Sprintf(" ▫️ Total interaksi : *%s pesan*\n", formatRibuan(totalMsg)))
	sb.WriteString(fmt.Sprintf(" ▫️ Rata-rata       : *%.1f* / %s\n", avgMsg, periodUnit))
	sb.WriteString(fmt.Sprintf(" ▫️ Titik teramai   : *%s* (*%s* pesan)\n\n", peakLabel, formatRibuan(peakVal)))

	// ── Breakdown jenis pesan (top 5) ──
	if len(kinds) > 0 {
		sb.WriteString("🧩 *JENIS PESAN*\n")
		total := 0
		for _, k := range kinds {
			total += k.Count
		}
		maxC := kinds[0].Count
		for i, k := range kinds {
			if i >= 5 {
				break
			}
			sb.WriteString(fmt.Sprintf(" %s %s `%s` %d%%\n",
				bar(k.Count, maxC, 8), padRight(src.KindLabel(k.Kind), 11),
				formatRibuan(k.Count), pct(k.Count, total)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("🛰️ *AKTIVITAS 7 HARI*\n")
	sb.WriteString(fmt.Sprintf(" ▫️ Total pesan     : *%s*\n", formatRibuan(live.Total)))
	sb.WriteString(fmt.Sprintf(" ▫️ Pengirim unik   : *%d*\n", live.UniqueSenders))
	sb.WriteString(fmt.Sprintf(" ▫️ Manusia vs Bot  : *%s* (%d%%) / *%s* (%d%%)\n", formatRibuan(live.HumanMsgs), humanPct, formatRibuan(live.BotMsgs), botPct))
	sb.WriteString(fmt.Sprintf(" ▫️ Pesan media     : *%s* (%d%%)\n", formatRibuan(live.Media), mediaPct))
	sb.WriteString(fmt.Sprintf(" ▫️ Aktif hari ini  : *%s*\n", formatRibuan(live.ActiveToday)))
	sb.WriteString(fmt.Sprintf(" ▫️ Device dominan  : *%s*\n", strings.ToUpper(topDev)))
	sb.WriteString(fmt.Sprintf(" ▫️ Jam tersibuk    : *%s*\n", peakHourLabel(live)))

	sb.WriteString("\n_Perintah lain:_ `gstats online` • `top` • `jam` • `jenis` • `help`")
	return ctx.Reply(sb.String())
}

// =================================================================
// ONLINE / LAST-SEEN (data langsung dari WA)
// =================================================================

func RenderOnlineReport(ctx *ContextBot, groupID string) error {
	gi, err := ctx.Client.GetGroupInfo(ctx.Ctx, ctx.ChatJID)
	if err != nil {
		return ctx.Reply("❌ Gagal mengambil info grup.")
	}

	// Subscribe presence member (dibatasi agar tak membanjiri server WA) lalu
	// beri waktu sebentar untuk update masuk. Prioritaskan yang belum diketahui.
	subs := make([]types.JID, 0, len(gi.Participants))
	for _, p := range gi.Participants {
		subs = append(subs, p.JID)
	}
	if len(subs) > 50 {
		subs = subs[:50]
	}
	go ctx.React("🛰️")
	src.RequestGroupPresence(ctx.Client, subs)
	time.Sleep(2500 * time.Millisecond)

	type row struct {
		name   string
		online bool
		seen   int64
	}
	var online, offline []row
	for _, p := range gi.Participants {
		info := participantPresence(p)
		name := memberName(p)
		if !info.Known {
			continue
		}
		r := row{name: name, online: info.Online, seen: info.LastSeen}
		if info.Online {
			online = append(online, r)
		} else {
			offline = append(offline, r)
		}
	}
	// Offline urut dari yang paling baru terlihat.
	sort.Slice(offline, func(i, j int) bool { return offline[i].seen > offline[j].seen })

	if len(online) == 0 && len(offline) == 0 {
		_ = ctx.React("📭")
		return ctx.Reply("📭 Belum ada data presence untuk grup ini.\n\nWhatsApp hanya mengirim status online untuk member yang sudah dipantau. Coba lagi beberapa menit setelah ada aktivitas chat.")
	}

	var sb strings.Builder
	sb.WriteString("🛰️ *STATUS ONLINE MEMBER*\n")
	sb.WriteString(fmt.Sprintf("🏢 %s\n\n", cleanText(gi.Name)))

	sb.WriteString(fmt.Sprintf("🟢 *Online sekarang (%d):*\n", len(online)))
	if len(online) == 0 {
		sb.WriteString(" _(tidak ada yang terpantau online)_\n")
	} else {
		sort.Slice(online, func(i, j int) bool { return online[i].name < online[j].name })
		for _, r := range online {
			sb.WriteString(fmt.Sprintf(" • %s\n", r.name))
		}
	}

	sb.WriteString("\n⚪ *Terakhir terlihat:*\n")
	if len(offline) == 0 {
		sb.WriteString(" _(belum ada data)_\n")
	} else {
		for i, r := range offline {
			if i >= 12 {
				sb.WriteString(fmt.Sprintf(" _…dan %d lainnya._\n", len(offline)-i))
				break
			}
			sb.WriteString(fmt.Sprintf(" • %s — %s\n", r.name, humanSince(r.seen)))
		}
	}

	sb.WriteString("\n_Catatan: hanya member yang dipantau (sering chat) yang muncul; member yang menyembunyikan last-seen tak tampil waktunya._")
	_ = ctx.React("✅")
	return ctx.Reply(sb.String())
}

// =================================================================
// HEATMAP PER JAM
// =================================================================

func RenderHourly(ctx *ContextBot, groupID, date, label string) error {
	data := src.DB.GetHourlyGroupStats(groupID, date)
	if len(data) == 0 {
		return ctx.Reply(fmt.Sprintf("📭 Tak ada data per jam untuk *%s*.", label))
	}
	hours := map[int]int{}
	total, maxV := 0, 0
	for _, d := range data {
		hours[d.Hour] = d.Message
		total += d.Message
		if d.Message > maxV {
			maxV = d.Message
		}
	}

	var sb strings.Builder
	sb.WriteString("🕒 *HEATMAP PER JAM*\n")
	sb.WriteString(fmt.Sprintf("📅 %s • total *%s* pesan\n\n", label, formatRibuan(total)))
	for h := 0; h <= 23; h++ {
		v := hours[h]
		sb.WriteString(fmt.Sprintf("`%02d` %s %s\n", h, bar(v, maxV, 12), formatRibuan(v)))
	}
	return ctx.Reply(sb.String())
}

// =================================================================
// BREAKDOWN JENIS PESAN
// =================================================================

func RenderKinds(ctx *ContextBot, groupID, dateLike, label string) error {
	kinds := src.DB.GetGroupKindBreakdown(groupID, dateLike)
	if len(kinds) == 0 {
		return ctx.Reply(fmt.Sprintf("📭 Tak ada data jenis pesan untuk *%s*.", label))
	}
	total, maxV := 0, kinds[0].Count
	for _, k := range kinds {
		total += k.Count
	}

	var sb strings.Builder
	sb.WriteString("🧩 *BREAKDOWN JENIS PESAN*\n")
	sb.WriteString(fmt.Sprintf("📅 %s • total *%s* pesan\n\n", label, formatRibuan(total)))
	for _, k := range kinds {
		sb.WriteString(fmt.Sprintf("%s %s\n   └ %s (%d%%)\n",
			bar(k.Count, maxV, 10), src.KindLabel(k.Kind), formatRibuan(k.Count), pct(k.Count, total)))
	}
	return ctx.Reply(sb.String())
}

// =================================================================
// LEADERBOARD
// =================================================================

// RenderTopMemberReport menampilkan leaderboard member teraktif. `limitArg` opsional (angka).
func RenderTopMemberReport(ctx *ContextBot, groupID, dateLabel, limitArg string) error {
	limit := 10
	if n, err := strconv.Atoi(strings.TrimSpace(limitArg)); err == nil && n >= 1 && n <= 30 {
		limit = n
	}
	topUsers := src.DB.GetTopGroupUsers(groupID, limit)
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
		// Indikator online bila JID-nya terpantau.
		dot := ""
		if jid, err := types.ParseJID(u.UserID); err == nil {
			if info := src.PresenceFor(jid); info.Known && info.Online {
				dot = " 🟢"
			}
		}
		sb.WriteString(fmt.Sprintf("%s *%s*%s\n", rank, name, dot))
		sb.WriteString(fmt.Sprintf("     └ %s pesan • %s media • %s kata\n", formatRibuan(u.MessageCount), formatRibuan(u.MediaCount), formatRibuan(u.WordCount)))
	}

	sb.WriteString(fmt.Sprintf("\n🛰️ *7 hari terakhir:* %s pesan • %d pengirim unik", formatRibuan(live.Total), live.UniqueSenders))
	return ctx.Reply(sb.String())
}

// =================================================================
// KARTU DETAIL PER-MEMBER
// =================================================================

func RenderMemberDetail(ctx *ContextBot, groupID string, target types.JID) error {
	userID := target.String()

	// Statistik akumulatif user di grup (cari di top list besar; cukup akurat).
	var stat *src.GroupUserStat
	for _, u := range src.DB.GetTopGroupUsers(groupID, 1000) {
		if sameUser(u.UserID, userID) {
			s := u
			stat = &s
			break
		}
	}

	kinds := src.DB.GetUserKindBreakdown(groupID, userID, "%")
	info := src.PresenceFor(target)

	name := strings.Split(target.User, "@")[0]
	if stat != nil && cleanText(stat.Name) != "" && stat.Name != "Unknown" {
		name = cleanText(stat.Name)
	}

	var sb strings.Builder
	sb.WriteString("👤 *DETAIL MEMBER*\n")
	sb.WriteString(fmt.Sprintf("📛 *Nama:* %s\n", name))
	sb.WriteString(fmt.Sprintf("🔖 *Nomor:* @%s\n", target.User))

	// Status presence.
	statusLine := "❔ Belum terpantau"
	if info.Known {
		if info.Online {
			statusLine = "🟢 *Online sekarang*"
		} else if info.LastSeen > 0 {
			statusLine = "⚪ Terakhir terlihat " + humanSince(info.LastSeen)
		} else {
			statusLine = "⚪ Offline (last-seen disembunyikan)"
		}
	}
	sb.WriteString("📡 *Status:* " + statusLine + "\n\n")

	if stat == nil {
		sb.WriteString("_Belum ada statistik pesan untuk member ini._")
		return replyMention(ctx, sb.String(), []string{userID})
	}

	sb.WriteString("📊 *Statistik (sepanjang masa):*\n")
	sb.WriteString(fmt.Sprintf(" ▫️ Total pesan : *%s*\n", formatRibuan(stat.MessageCount)))
	sb.WriteString(fmt.Sprintf(" ▫️ Media       : *%s*\n", formatRibuan(stat.MediaCount)))
	sb.WriteString(fmt.Sprintf(" ▫️ Total kata  : *%s*\n", formatRibuan(stat.WordCount)))
	if stat.MessageCount > 0 {
		sb.WriteString(fmt.Sprintf(" ▫️ Rata kata   : *%.1f* /pesan\n", float64(stat.WordCount)/float64(stat.MessageCount)))
	}
	if !stat.LastActive.IsZero() {
		sb.WriteString(fmt.Sprintf(" ▫️ Aktif terakhir : *%s*\n", humanSince(stat.LastActive.Unix())))
	}

	if len(kinds) > 0 {
		sb.WriteString("\n🧩 *Jenis pesan:*\n")
		maxV := kinds[0].Count
		for i, k := range kinds {
			if i >= 6 {
				break
			}
			sb.WriteString(fmt.Sprintf(" %s %s `%s`\n", bar(k.Count, maxV, 6), padRight(src.KindLabel(k.Kind), 11), formatRibuan(k.Count)))
		}
	}

	return replyMention(ctx, sb.String(), []string{userID})
}

// =================================================================
// HELPER
// =================================================================

func gstatsHelp() string {
	return "📊 *PANDUAN GSTATS*\n\n" +
		"• `gstats` — dashboard hari ini\n" +
		"• `gstats yesterday` / `gstats 15` / `gstats 2026-06-30` — dashboard tanggal\n" +
		"• `gstats total` — rekap bulan ini\n" +
		"• `gstats online` — siapa online sekarang + last-seen\n" +
		"• `gstats top [N]` — leaderboard member\n" +
		"• `gstats jam [tanggal]` — heatmap per jam\n" +
		"• `gstats jenis [tanggal]` — breakdown jenis pesan\n" +
		"• `gstats @user` / reply pesan — detail satu member"
}

// resolveStatsTarget mendeteksi target member dari mention atau reply.
func resolveStatsTarget(ctx *ContextBot, args string) (types.JID, bool) {
	ci := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo()
	if ci != nil {
		if m := ci.GetMentionedJID(); len(m) > 0 {
			if jid, err := types.ParseJID(m[0]); err == nil {
				return jid, true
			}
		}
		// Reply ke pesan member + ada kata kunci eksplisit (hindari tabrakan dgn dashboard).
		if p := ci.GetParticipant(); p != "" {
			if args == "" || strings.HasPrefix(args, "cek") || strings.HasPrefix(args, "member") || strings.HasPrefix(args, "user") {
				if jid, err := types.ParseJID(p); err == nil {
					return jid, true
				}
			}
		}
	}
	return types.JID{}, false
}

// participantPresence mengambil presence sebuah participant, mencoba beberapa
// representasi JID (primary/PN/LID) karena event presence biasanya ber-nomor (PN).
func participantPresence(p types.GroupParticipant) src.PresenceInfo {
	if info := src.PresenceFor(p.JID); info.Known {
		return info
	}
	if !p.PhoneNumber.IsEmpty() {
		if info := src.PresenceFor(p.PhoneNumber); info.Known {
			return info
		}
	}
	if !p.LID.IsEmpty() {
		if info := src.PresenceFor(p.LID); info.Known {
			return info
		}
	}
	return src.PresenceInfo{}
}

func memberName(p types.GroupParticipant) string {
	if p.DisplayName != "" {
		return cleanText(p.DisplayName)
	}
	if !p.PhoneNumber.IsEmpty() {
		return "@" + p.PhoneNumber.User
	}
	return "@" + p.JID.User
}

// sameUser membandingkan dua JID string secara longgar (berdasarkan nomor/User).
func sameUser(a, b string) bool {
	if a == b {
		return true
	}
	ja, ea := types.ParseJID(a)
	jb, eb := types.ParseJID(b)
	if ea == nil && eb == nil {
		return ja.User == jb.User
	}
	return false
}

// parseDateArg → (statDate "YYYY-MM-DD", label) untuk query per-hari.
func parseDateArg(arg string) (string, string) {
	now := time.Now()
	arg = strings.TrimSpace(strings.ToLower(arg))
	switch {
	case arg == "":
		return now.Format("2006-01-02"), "Hari ini"
	case arg == "yesterday" || arg == "kemarin":
		y := now.AddDate(0, 0, -1)
		return y.Format("2006-01-02"), y.Format("02 Jan 2006")
	case regexp.MustCompile(`^\d{1,2}$`).MatchString(arg):
		if d, _ := strconv.Atoi(arg); d >= 1 && d <= 31 {
			c := time.Date(now.Year(), now.Month(), d, 0, 0, 0, 0, now.Location())
			return c.Format("2006-01-02"), c.Format("02 Jan 2006")
		}
	case regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(arg):
		if p, err := time.Parse("2006-01-02", arg); err == nil {
			return arg, p.Format("02 Jan 2006")
		}
	}
	return now.Format("2006-01-02"), "Hari ini"
}

// parseDateLike → (pola LIKE statDate, label) untuk breakdown jenis.
func parseDateLike(arg string) (string, string) {
	now := time.Now()
	arg = strings.TrimSpace(strings.ToLower(arg))
	switch {
	case arg == "all" || arg == "semua":
		return "%", "Sepanjang masa"
	case arg == "total" || arg == "bulan ini":
		return now.Format("2006-01") + "-%", "Bulan " + now.Format("Jan 2006")
	default:
		d, label := parseDateArg(arg)
		return d, label
	}
}

// (helper `bar` dipakai bersama dari dashboard.go)

// padRight memastikan lebar minimal kolom label (berbasis rune).
func padRight(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(r))
}

// humanSince memformat selisih waktu sejak unix detik (mis. "5 mnt lalu").
func humanSince(unix int64) string {
	if unix <= 0 {
		return "tak diketahui"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "baru saja"
	case d < time.Hour:
		return fmt.Sprintf("%d mnt lalu", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d jam lalu", int(d.Hours()))
	default:
		return fmt.Sprintf("%d hari lalu", int(d.Hours()/24))
	}
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

// replyMention mengirim teks dengan menandai (mention) JID tertentu.
func replyMention(ctx *ContextBot, text string, mentions []string) error {
	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: &waProto.ContextInfo{MentionedJID: mentions},
		},
	}
	_, err := ctx.Client.SendMessage(context.Background(), ctx.ChatJID, msg, src.AndroidExtra())
	return err
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
