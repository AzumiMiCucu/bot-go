package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"bot-go/src"
)

func init() {
	RegisterCommand(Command{
		Name:        "Help Menu",
		Category:    "General",
		Aliases:     []string{"help", "menu", "list"},
		Pattern:     regexp.MustCompile(`(?i)^\s*(?:help|menu|list|fitur|bantuan)\s*$`),
		Description: "Menampilkan daftar keahlian bot",
		//	Cooldown:    5 * time.Second,
		Execute: ExecuteHelpMenu,
	})
}

// Hanya fallback karena kita tak ada akses db dari luar

func ExecuteHelpMenu(ctx *ContextBot) error {
	userIsAdmin, _ := isUserAdmin(ctx)

	categories := make(map[string][]*Command)
	for _, cmd := range src.CommandRegistry {
		cat := cmd.Category
		catLower := strings.ToLower(cat)

		switch catLower {
		case "owner":
			if !ctx.IsOwner {
				continue
			}
		case "group":
			if !userIsAdmin && !ctx.IsOwner {
				continue
			}
		}

		if cat == "System" {
			continue
		}
		categories[cat] = append(categories[cat], cmd)
	}

	orderMap := map[string]int{
		"General":    0,
		"Tools":      1,
		"Downloader": 2,
		"Premium":    3,
		"Kasino":     4,
		"Group":      5,
		"Owner":      99,
	}

	var cats []string
	for c := range categories {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool {
		oi, oki := orderMap[cats[i]]
		oj, okj := orderMap[cats[j]]
		if !oki {
			oi = 50
		}
		if !okj {
			oj = 50
		}
		if oi != oj {
			return oi < oj
		}
		return cats[i] < cats[j]
	})

	catEmoji := map[string]string{
		"General":    "🌐",
		"Tools":      "🛠️",
		"Downloader": "⬇️",
		"Premium":    "💰",
		"Kasino":     "🎰",
		"Group":      "👥",
		"Owner":      "👑",
		"Finder":     "🔎",
		"Game":       "🎮",
		"Lainnya":    "📦",
	}

	totalCmd := 0
	for _, cmds := range categories {
		totalCmd += len(cmds)
	}

	roleLabel := "👤 User"
	if ctx.IsOwner {
		roleLabel = "👑 Owner"
	} else if userIsAdmin {
		roleLabel = "🛡️ Admin"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		"✨ *BOLANG SERVICES* ✨\n"+
			"%s, *%s*!\n"+
			"_%s · %d fitur aktif_\n",
		greeting(), ctx.PushName, roleLabel, totalCmd,
	))

	for _, cat := range cats {
		cmds := categories[cat]
		if len(cmds) == 0 {
			continue
		}

		emoji := catEmoji[cat]
		if emoji == "" {
			emoji = "📁"
		}

		sb.WriteString(fmt.Sprintf("\n%s *%s*\n", emoji, strings.ToUpper(cat)))

		for _, cmd := range cmds {
			triggers := cmd.Aliases
			if len(triggers) == 0 {
				triggers = []string{strings.ToLower(cmd.Name)}
			}

			shown := triggers
			suffix := ""
			if len(shown) > 3 {
				shown = triggers[:3]
				suffix = fmt.Sprintf(" _+%d_", len(triggers)-3)
			}

			// Tambahkan info cooldown (jika ada) ke deskripsi menu
			cdInfo := ""
			if cmd.Cooldown > 0 {
				cdInfo = fmt.Sprintf(" ⏱️(%vm)", int(cmd.Cooldown.Minutes()))
			}

			sb.WriteString(fmt.Sprintf(
				"  • *%s*%s\n    `%s`%s\n",
				cmd.Name, cdInfo, strings.Join(shown, "` `"), suffix,
			))
		}
	}

	sb.WriteString("\n💡 Ketik kata kunci saja.\n")
	sb.WriteString("_Contoh:_ `developer`")

	return ctx.Reply(sb.String())
}

func greeting() string {
	h := time.Now().UTC().Add(7 * time.Hour).Hour()
	switch {
	case h >= 4 && h < 11:
		return "Selamat pagi"
	case h >= 11 && h < 15:
		return "Selamat siang"
	case h >= 15 && h < 18:
		return "Selamat sore"
	default:
		return "Selamat malam"
	}
}
