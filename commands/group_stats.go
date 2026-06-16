package commands

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-go/src"

	"github.com/fogleman/gg"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func init() {
	RegisterCommand(Command{
		Name:        "Group Stats",
		Category:    "Group",
		Aliases:     []string{"gstats", "grupstats"},
		Pattern:     regexp.MustCompile(`(?i)^(?:gstats|grupstats)(?:\s+(.+))?$`),
		Description: "Menampilkan analisis statistik grup dan Top Member",
		Execute:     ExecuteGroupStats,
	}).Use(GroupOnlyMiddleware)
}

const FontPath = "src/arialuni.ttf"

// --- COLOR PALETTE (Modern Dark Theme) ---
var (
	BgColor       = color.RGBA{26, 27, 38, 255}    // Dark Blue/Grey Background
	CardColor     = color.RGBA{36, 40, 59, 255}    // Lighter Panel
	CardColorAlt  = color.RGBA{41, 46, 66, 255}    // Hover Panel
	PrimaryColor  = color.RGBA{0, 194, 255, 255}   // Cyan Accent
	TextColor     = color.RGBA{192, 202, 245, 255} // Light Blueish White
	TextMuted     = color.RGBA{122, 136, 207, 255} // Muted Text
	GridLineColor = color.RGBA{50, 52, 74, 255}    // Subtle Grid lines
	
	GoldColor   = color.RGBA{255, 193, 7, 255}
	SilverColor = color.RGBA{189, 195, 199, 255}
	BronzeColor = color.RGBA{205, 127, 50, 255}
)
// Regex ini HANYA mendeteksi Emoji, TIDAK akan menghapus Arab/Jepang/Korea
var emojiRegex = regexp.MustCompile(`[\x{1F600}-\x{1F64F}\x{1F300}-\x{1F5FF}\x{1F680}-\x{1F6FF}\x{1F700}-\x{1F77F}\x{1F780}-\x{1F7FF}\x{1F800}-\x{1F8FF}\x{1F900}-\x{1F9FF}\x{1FA00}-\x{1FA6F}\x{1FA70}-\x{1FAFF}\x{2600}-\x{26FF}\x{2700}-\x{27BF}\x{2300}-\x{23FF}\x{2B50}\x{1F004}\x{1F0CF}\x{25AA}\x{25AB}\x{25B6}\x{25C0}\x{25FB}-\x{25FE}\x{FE0F}]`)

func cleanText(s string) string {
	// Menghapus emoji, tapi membiarkan karakter khusus dari negara lain
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
		if args == "top" {
			// GetTopGroupUsers mengambil data akumulatif sepanjang masa,
			// jadi label periode harus mencerminkan itu (bukan tanggal hari ini).
			return RenderTopMemberReport(ctx, groupID, "Sepanjang Masa")
		} else if args == "all" || args == "total" || args == "bulan ini" {
			isTotalMode = true
			displayDate = "Total Seluruh Data"
		} else if args == "yesterday" {
			yesterday := now.AddDate(0, 0, -1)
			targetDate = yesterday.Format("2006-01-02")
			displayDate = yesterday.Format("02 Jan 2006")
		} else if regexp.MustCompile(`^\d{1,2}$`).MatchString(args) {
			dayNum, _ := strconv.Atoi(args)
			if dayNum >= 1 && dayNum <= 31 {
				customDate := time.Date(now.Year(), now.Month(), dayNum, 0, 0, 0, 0, now.Location())
				targetDate = customDate.Format("2006-01-02")
				displayDate = customDate.Format("02 Jan 2006")
			}
		} else if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(args) {
			targetDate = args
			parsedTime, _ := time.Parse("2006-01-02", args)
			displayDate = parsedTime.Format("02 Jan 2006")
		}
	}

	groupInfo, err := ctx.Client.GetGroupInfo(ctx.Ctx, ctx.ChatJID)
	groupName := "Unknown Group"
	memberCount := 0
	var admins []string

	if err == nil {
		groupName = cleanText(groupInfo.Name) // Bersihkan emoji dari nama grup
		memberCount = len(groupInfo.Participants)
		for _, p := range groupInfo.Participants {
			if p.IsAdmin || p.IsSuperAdmin {
				adminName := "+" + p.JID.User
				if ctx.Client.Store != nil && ctx.Client.Store.Contacts != nil {
					if contact, errContact := ctx.Client.Store.Contacts.GetContact(ctx.Ctx, p.JID); errContact == nil {
						if contact.PushName != "" {
							adminName = contact.PushName
						} else if contact.FullName != "" {
							adminName = contact.FullName
						}
					}
				}
				adminName = cleanText(adminName) // Bersihkan emoji dari nama admin
				if len(adminName) > 13 {
					adminName = adminName[:11] + ".."
				}
				admins = append(admins, adminName)
			}
		}
	}

	var ppImage image.Image
	ppInfo, err := ctx.Client.GetProfilePictureInfo(ctx.Ctx, ctx.ChatJID, &whatsmeow.GetProfilePictureParams{Preview: false})
	if err == nil && ppInfo != nil && ppInfo.URL != "" {
		resp, errHTTP := http.Get(ppInfo.URL)
		if errHTTP == nil {
			defer resp.Body.Close()
			imgDecoded, _, errDec := image.Decode(resp.Body)
			if errDec == nil {
				ppImage = resizeImage(squareCrop(imgDecoded), 200, 200)
			}
		}
	}

	chartData := make([]int, 0)
	labels := make([]string, 0)
	totalMsg := 0
	peakVal := 0
	peakLabel := ""

	if isTotalMode {
		isDailyMode = true
		dailyStats := src.DB.GetMonthlyGroupStats(groupID, now.Month(), now.Year())
		dataMap := make(map[int]int)
		for _, d := range dailyStats {
			dataMap[d.Day] = d.Message
		}
		for i := 1; i <= 31; i++ {
			val := dataMap[i]
			chartData = append(chartData, val)
			labels = append(labels, strconv.Itoa(i))
			totalMsg += val
			if val > peakVal {
				peakVal = val
				peakLabel = strconv.Itoa(i)
			}
		}
	} else {
		hourlyStats := src.DB.GetHourlyGroupStats(groupID, targetDate)
		dataMap := make(map[int]int)
		for _, d := range hourlyStats {
			dataMap[d.Hour] = d.Message
		}
		for i := 0; i <= 23; i++ {
			val := dataMap[i]
			chartData = append(chartData, val)
			labels = append(labels, strconv.Itoa(i))
			totalMsg += val
			if val > peakVal {
				peakVal = val
				peakLabel = strconv.Itoa(i)
			}
		}
	}

	if totalMsg == 0 {
		return ctx.Reply(fmt.Sprintf("📭 Belum ada data interaksi pesan yang terekam pada periode ini (%s).", displayDate))
	}

	canvasW, canvasH := 900, 1200
	dc := gg.NewContext(canvasW, canvasH)

	dc.SetColor(BgColor)
	dc.Clear()

	// 1. HEADER CARD
	dc.SetColor(CardColor)
	dc.DrawRoundedRectangle(40, 40, float64(canvasW-80), 260, 20)
	dc.Fill()

	// Draw Profile Picture (Circular)
	avatarRadius := 80.0
	avatarX, avatarY := 160.0, 170.0
	if ppImage != nil {
		dc.DrawCircle(avatarX, avatarY, avatarRadius)
		dc.Clip()
		dc.DrawImageAnchored(ppImage, int(avatarX), int(avatarY), 0.5, 0.5)
		dc.ResetClip()
	} else {
		dc.SetColor(CardColorAlt)
		dc.DrawCircle(avatarX, avatarY, avatarRadius)
		dc.Fill()
		_ = dc.LoadFontFace(FontPath, 20)
		dc.SetColor(TextMuted)
		dc.DrawStringAnchored("NO IMAGE", avatarX, avatarY, 0.5, 0.5)
	}

	// Avatar Border
	dc.DrawCircle(avatarX, avatarY, avatarRadius)
	dc.SetLineWidth(4)
	dc.SetColor(PrimaryColor)
	dc.Stroke()

	// Group Info Text
	textX := 300.0
	if len(groupName) > 22 {
		groupName = groupName[:20] + "..."
	}
	_ = dc.LoadFontFace(FontPath, 45)
	dc.SetColor(TextColor)
	dc.DrawString(groupName, textX, 120)

	// Dihapus emoji untuk menghindari kotak-kotak
	_ = dc.LoadFontFace(FontPath, 22)
	dc.SetColor(TextMuted)
	dc.DrawString(fmt.Sprintf("%d MEMBERS   •   %d ADMINS", memberCount, len(admins)), textX, 170)

	// Stat Badge
	dc.SetColor(PrimaryColor)
	dc.DrawRoundedRectangle(textX, 200, 300, 45, 10)
	dc.Fill()
	_ = dc.LoadFontFace(FontPath, 18)
	dc.SetColor(BgColor)
	dc.DrawStringAnchored(fmt.Sprintf("TOTAL MESSAGES: %s", formatRibuan(totalMsg)), textX+150, 222, 0.5, 0.5)

	// 2. CHART CARD
	chartYStart := 330.0
	chartHeight := 500.0
	dc.SetColor(CardColor)
	dc.DrawRoundedRectangle(40, chartYStart, float64(canvasW-80), chartHeight, 20)
	dc.Fill()

	_ = dc.LoadFontFace(FontPath, 24)
	dc.SetColor(TextColor)
	dc.DrawStringAnchored("MESSAGE ANALYTICS", float64(canvasW)/2, chartYStart+40, 0.5, 0.5)

	maxValFloat := float64(peakVal)
	if maxValFloat == 0 {
		maxValFloat = 10
	}
	magnitude := math.Pow(10, math.Floor(math.Log10(maxValFloat)))
	yMaxBound := math.Ceil(maxValFloat/magnitude) * magnitude
	if yMaxBound < maxValFloat*1.2 {
		yMaxBound += magnitude
	}

	graphBottom := chartYStart + 430.0
	graphTop := chartYStart + 100.0
	graphLeft := 100.0
	graphRight := float64(canvasW) - 80.0
	graphWidth := graphRight - graphLeft
	graphAvailHeight := graphBottom - graphTop

	// Draw Y-Axis lines
	_ = dc.LoadFontFace(FontPath, 14)
	steps := 5
	for i := 0; i <= steps; i++ {
		fraction := float64(i) / float64(steps)
		yPos := graphBottom - (fraction * graphAvailHeight)
		valText := int(fraction * yMaxBound)

		dc.SetColor(GridLineColor)
		dc.SetLineWidth(1.5)
		dc.DrawLine(graphLeft, yPos, graphRight, yPos)
		dc.Stroke()

		dc.SetColor(TextMuted)
		dc.DrawStringAnchored(formatRibuan(valText), graphLeft-20, yPos, 1.0, 0.5)
	}

	// X-Axis Label
	bottomLabel := "TIME (HOURS)"
	if isDailyMode {
		bottomLabel = "DATE (DAY)"
	}
	dc.SetColor(TextMuted)
	dc.DrawStringAnchored(bottomLabel, graphLeft+(graphWidth/2), graphBottom+45, 0.5, 0.5)

	// Draw Bars
	numBars := len(chartData)
	barSpacing := 6.0
	barWidth := (graphWidth / float64(numBars)) - barSpacing

	for i, val := range chartData {
		xBase := graphLeft + (float64(i) * (barWidth + barSpacing)) + (barSpacing / 2)
		_ = dc.LoadFontFace(FontPath, 12)

		if !isDailyMode || (isDailyMode && i%2 == 0) {
			dc.SetColor(TextMuted)
			dc.DrawStringAnchored(labels[i], xBase+(barWidth/2), graphBottom+20, 0.5, 0.5)
		}

		if val > 0 {
			barHeight := (float64(val) / yMaxBound) * graphAvailHeight
			yBase := graphBottom - barHeight

			if val == peakVal {
				dc.SetColor(PrimaryColor) 
			} else {
				dc.SetColor(color.RGBA{59, 130, 246, 255}) 
			}

			dc.DrawRoundedRectangle(xBase, yBase, barWidth, barHeight, barWidth/3)
			dc.Fill()

			if numBars <= 24 || val == peakVal {
				dc.SetColor(TextColor)
				dc.DrawStringAnchored(strconv.Itoa(val), xBase+(barWidth/2), yBase-10, 0.5, 0.5)
			}
		}
	}

	// 3. ADMIN LIST CARD
	// 3. ADMIN LIST CARD
	adminYStart := chartYStart + chartHeight + 30
	adminHeight := float64(canvasH) - adminYStart - 60
	dc.SetColor(CardColor)
	dc.DrawRoundedRectangle(40, adminYStart, float64(canvasW-80), adminHeight, 20)
	dc.Fill()

	_ = dc.LoadFontFace(FontPath, 20)
	dc.SetColor(TextColor)
	dc.DrawStringAnchored("GROUP ADMINISTRATORS", float64(canvasW)/2, adminYStart+35, 0.5, 0.5)

	_ = dc.LoadFontFace(FontPath, 16)
	
	// PERBAIKAN: Hitung lebar kolom dengan padding yang pas
	colWidth := (float64(canvasW) - 140) / 3.0 // Dibagi 3 kolom
	startX := 90.0 // Posisi X mulai dari kiri (padding)

	adminRow := 0
	for i, adminName := range admins {
		if i > 11 { // Saya naikkan limitnya jadi 12 admin (4 baris) agar space kosong terpakai
			break
		}
		colIndex := i % 3
		if colIndex == 0 && i != 0 {
			adminRow++
		}

		// PERBAIKAN: Set posisi X berdasarkan urutan kolom (Rata Kiri)
		xPos := startX + (float64(colIndex) * colWidth)
		yPos := adminYStart + 90 + float64(adminRow*35)

		// Titik indikator (Bullet Point)
		dc.SetColor(PrimaryColor)
		dc.DrawCircle(xPos, yPos-4, 4)
		dc.Fill()

		// Teks Nama Admin dengan anchor `0.0` (Artinya Rata Kiri, bukan di tengah)
		dc.SetColor(TextColor)
		dc.DrawStringAnchored(adminName, xPos+15, yPos, 0.0, 0.5)
	}

	avgMsg := 0.0
	if len(chartData) > 0 {
		avgMsg = float64(totalMsg) / float64(len(chartData))
	}

	_ = dc.LoadFontFace(FontPath, 14)
	dc.SetColor(TextMuted)
	dc.DrawStringAnchored("Group Report by Bolang", float64(canvasW)/2, float64(canvasH)-25, 0.5, 0.5)

	caption := fmt.Sprintf(`📊 *Laporan Analitik Grup*
🏢 *Grup:* %s
📅 *Periode Data:* %s

📈 *Ringkasan Trafik:*
 ▫️ Total Interaksi : *%s pesan*
 ▫️ Rata-rata Masuk : *%.1f* pesan per %s
 ▫️ Titik Teramai   : Pukul/Tgl *%s* (Tembus *%s* pesan)
`, groupName, displayDate, formatRibuan(totalMsg), avgMsg, strings.ToLower(bottomLabel), peakLabel, formatRibuan(peakVal))

	return sendCanvasImage(ctx, dc, caption)
}

// --- DESAIN BARU: TOP MEMBER PODIUM & LIST ---
func RenderTopMemberReport(ctx *ContextBot, groupID string, dateLabel string) error {
	topUsers := src.DB.GetTopGroupUsers(groupID, 10)
	if len(topUsers) == 0 {
		return ctx.Reply("📭 Belum ada data interaksi yang cukup untuk menampilkan Top Member.")
	}

	W, H := 900, 1200
	dc := gg.NewContext(W, H)

	dc.SetColor(BgColor)
	dc.Clear()

	// HEADER
	dc.SetColor(PrimaryColor)
	_ = dc.LoadFontFace(FontPath, 50)
	dc.DrawStringAnchored("LEADERBOARD", float64(W)/2, 80, 0.5, 0.5)

	dc.SetColor(TextColor)
	_ = dc.LoadFontFace(FontPath, 28)
	dc.DrawStringAnchored("MOST ACTIVE MEMBERS", float64(W)/2, 130, 0.5, 0.5)

	dc.SetColor(TextMuted)
	_ = dc.LoadFontFace(FontPath, 18)
	dc.DrawStringAnchored("Periode: "+dateLabel, float64(W)/2, 165, 0.5, 0.5)

	// HELPER UNTUK PODIUM TOP 3
	drawPodium := func(rank int, name string, msgCount string, x, topY float64, col color.RGBA) {
		pw := 160.0    // Lebar podium
		baseY := 520.0 // Batas bawah rata untuk semua podium

		// Gambar Balok Podium (rounded atasnya saja)
		dc.SetColor(col)
		dc.DrawRoundedRectangle(x-pw/2, topY, pw, baseY-topY, 15)
		dc.Fill()
		// Timpa kotak di bawah supaya nampak datar di tanah
		dc.DrawRectangle(x-pw/2, baseY-15, pw, 15)
		dc.Fill()

		// Teks Rank (1, 2, 3) yang besar di dalam podium
		dc.SetColor(BgColor)
		_ = dc.LoadFontFace(FontPath, 65)
		dc.DrawStringAnchored(strconv.Itoa(rank), x, topY+(baseY-topY)/2, 0.5, 0.5)

		// Teks Nama (Dibersihkan dari emoji supaya tak kotak-kotak)
		name = cleanText(name)
		if len(name) > 13 {
			name = name[:11] + ".."
		}
		dc.SetColor(TextColor)
		_ = dc.LoadFontFace(FontPath, 24)
		dc.DrawStringAnchored(name, x, topY-30, 0.5, 0.5)

		// Teks Skor (Pesan)
		dc.SetColor(PrimaryColor)
		_ = dc.LoadFontFace(FontPath, 20)
		dc.DrawStringAnchored(msgCount+" Msg", x, topY-60, 0.5, 0.5)
	}

	// DRAW PODIUMS (2, 1, 3 order to look like a stage)
	if len(topUsers) >= 2 {
		drawPodium(2, topUsers[1].Name, formatRibuan(topUsers[1].MessageCount), 260, 370, SilverColor)
	}
	if len(topUsers) >= 1 {
		drawPodium(1, topUsers[0].Name, formatRibuan(topUsers[0].MessageCount), 450, 290, GoldColor)
	}
	if len(topUsers) >= 3 {
		drawPodium(3, topUsers[2].Name, formatRibuan(topUsers[2].MessageCount), 640, 410, BronzeColor)
	}

	// DRAW LIST CARD UNTUK RANK 4 HINGGA 10
	listStartY := 560.0
	for i := 3; i < len(topUsers); i++ {
		user := topUsers[i]
		rankStr := fmt.Sprintf("#%d", i+1)
		
		name := cleanText(user.Name) // Bersihkan Emoji
		if name == "" {
			name = strings.Split(user.UserID, "@")[0]
		}
		if len(name) > 25 {
			name = name[:23] + "..."
		}
		msgCount := formatRibuan(user.MessageCount)

		yPos := listStartY + float64((i-3)*80)

		// Background List Card
		dc.SetColor(CardColor)
		dc.DrawRoundedRectangle(80, yPos, float64(W-160), 65, 15)
		dc.Fill()

		// Aksen Garis di kiri Card
		dc.SetColor(CardColorAlt)
		dc.DrawRoundedRectangle(80, yPos, 60, 65, 15)
		dc.Fill()
		dc.DrawRectangle(120, yPos, 20, 65) 
		dc.Fill()

		// Teks Rank
		_ = dc.LoadFontFace(FontPath, 22)
		dc.SetColor(TextMuted)
		dc.DrawStringAnchored(rankStr, 110, yPos+32, 0.5, 0.5)

		// Teks Nama
		_ = dc.LoadFontFace(FontPath, 24)
		dc.SetColor(TextColor)
		dc.DrawStringAnchored(name, 160, yPos+32, 0, 0.5)

		// Teks Skor Message
		_ = dc.LoadFontFace(FontPath, 24)
		dc.SetColor(PrimaryColor)
		dc.DrawStringAnchored(msgCount+" Msg", float64(W)-110, yPos+32, 1.0, 0.5)
	}

	// FOOTER
	_ = dc.LoadFontFace(FontPath, 16)
	dc.SetColor(TextMuted)
	dc.DrawStringAnchored("Group Report by Bolang", float64(W)/2, float64(H)-30, 0.5, 0.5)

	return sendCanvasImage(ctx, dc, "`Leaderboard Member Teraktif`")
}

func sendCanvasImage(ctx *ContextBot, dc *gg.Context, caption string) error {
	var buf bytes.Buffer
	_ = dc.EncodePNG(&buf)
	imgBytes := buf.Bytes()

	ctxUpload, cancel := context.WithTimeout(ctx.Ctx, 20*time.Second)
	defer cancel()

	resp, err := ctx.Client.Upload(ctxUpload, imgBytes, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("gagal mengunggah gambar canvas: %v", err)
	}

	senderStr := ctx.Msg.Info.Sender.ToNonAD().String()
	if !ctx.Msg.Info.MessageSource.SenderAlt.IsEmpty() {
		senderStr = ctx.Msg.Info.MessageSource.SenderAlt.ToNonAD().String()
	}

	protoMsg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			Caption:       proto.String(caption),
			URL:           proto.String(resp.URL),
			DirectPath:    proto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			Mimetype:      proto.String("image/png"),
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(imgBytes))),
			ContextInfo: &waProto.ContextInfo{
				StanzaID:      proto.String(ctx.Msg.Info.ID),
				Participant:   proto.String(senderStr),
				QuotedMessage: ctx.Msg.Message,
			},
		},
	}

	_, err = ctx.Client.SendMessage(ctx.Ctx, ctx.ChatJID.ToNonAD(), protoMsg)
	return err
}

func squareCrop(img image.Image) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	size := w
	if h < w {
		size = h
	}
	x0 := (w - size) / 2
	y0 := (h - size) / 2

	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		return si.SubImage(image.Rect(x0, y0, x0+size, y0+size))
	}
	return img
}

func resizeImage(img image.Image, targetW, targetH int) image.Image {
	bounds := img.Bounds()
	ratioX := float64(bounds.Dx()) / float64(targetW)
	ratioY := float64(bounds.Dy()) / float64(targetH)

	newImg := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	for y := 0; y < targetH; y++ {
		for x := 0; x < targetW; x++ {
			srcX := int(float64(x) * ratioX)
			srcY := int(float64(y) * ratioY)
			newImg.Set(x, y, img.At(bounds.Min.X+srcX, bounds.Min.Y+srcY))
		}
	}
	return newImg
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