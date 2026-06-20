package commands

import (
    "fmt"
    "regexp"
    "strings"
    "time"

    "bot-go/src"
    "go.mau.fi/whatsmeow/types"
)

type PendingMatch struct {
    ChallengerJID string
    TargetJID     string
    GroupJID      string
    Timestamp     time.Time
}

var (
    pendingMatches = make(map[string]PendingMatch)
)

func init() {
    // Auto-cleanup tantangan yang tidak dijawab dalam 3 menit
    go func() {
        for {
            time.Sleep(1 * time.Minute)
            src.SessionMutex.Lock()
            for targetNumber, match := range pendingMatches {
                if time.Since(match.Timestamp) > 3*time.Minute {
                    delete(pendingMatches, targetNumber)
                }
            }
            src.SessionMutex.Unlock()
        }
    }()

    RegisterCommand(src.Command{
        Name:        "Ular Tangga",
        Aliases:     []string{"ut", "terimaut", "tolakut"},
        Category:    "Game",
        Pattern:     regexp.MustCompile(`(?i)^\s*(ulartangga|ut|terimaut|tolakut|stopulartangga|stoput)\b`),
        Description: "Permainan ular tangga multiplayer. Command: ut, terimaut, tolakut, stoput",
        Execute: func(ctx *src.ContextBot) error {

            var text string
            if ctx.Msg.Message.GetExtendedTextMessage() != nil {
                text = ctx.Msg.Message.GetExtendedTextMessage().GetText()
            } else {
                text = ctx.Msg.Message.GetConversation()
            }

            parts := strings.Fields(strings.ToLower(text))
            if len(parts) == 0 {
                return nil
            }
            cmd := parts[0]
            if strings.ContainsAny(string(cmd[0]), "./!#") {
                cmd = cmd[1:]
            }

            senderJIDStr := ctx.SenderJID.ToNonAD().String()
            senderNumber := strings.Split(senderJIDStr, "@")[0]
            var groupJIDStr string
            if ctx.IsGroup {
                groupJIDStr = ctx.ChatJID.ToNonAD().String()
            }

            switch cmd {

            case "ulartangga", "ut":
                if !ctx.IsGroup {
                    return ctx.Reply("❌ Perintah ini hanya bisa digunakan di dalam grup.")
                }

                var targetJID string
                if ctxInfo := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo(); ctxInfo != nil {
                    mentions := ctxInfo.GetMentionedJID()
                    if len(mentions) > 0 {
                        targetJID = mentions[0]
                    }
                }

                if targetJID == "" {
                    return ctx.Reply("⚠️ *Tag teman yang ingin kamu tantang!*\n\nContoh: `.ut @teman`")
                }
                if targetJID == senderJIDStr {
                    return ctx.Reply("⚠️ Kamu tidak bisa menantang diri sendiri.")
                }

                targetNumber := strings.Split(targetJID, "@")[0]

                src.SessionMutex.Lock()
                if src.ActivePlayers[senderNumber] {
                    src.SessionMutex.Unlock()
                    return ctx.Reply("⚠️ Kamu masih memiliki sesi permainan aktif!\n\nKetik `.stoput` untuk menghapus sesi.")
                }
                if src.ActivePlayers[targetNumber] {
                    src.SessionMutex.Unlock()
                    return ctx.Reply("⚠️ Teman yang kamu tag sedang bermain dengan orang lain.")
                }
                if _, exists := pendingMatches[targetNumber]; exists {
                    src.SessionMutex.Unlock()
                    return ctx.Reply("⚠️ Temanmu sedang ditantang oleh pemain lain.\n\nTunggu sampai dia merespon!")
                }

                pendingMatches[targetNumber] = PendingMatch{
                    ChallengerJID: senderJIDStr,
                    TargetJID:     targetJID,
                    GroupJID:      groupJIDStr,
                    Timestamp:     time.Now(),
                }
                src.SessionMutex.Unlock()

                pesan := fmt.Sprintf("🎲 *TANTANGAN ULAR TANGGA*\n\n@%s menantang @%s untuk bermain!\n\n⏱️ Ketik `terimaut` atau `tolakut`\n_(Kadaluarsa dalam 3 menit)_", senderNumber, targetNumber)
                ctx.Reply(pesan)
                ctx.Print(senderNumber, targetNumber)
             /*   btn := ctx.Button()
                btn.SetBody(pesan)
                btn.AddMentions([]string{senderJIDStr, targetJID})
                return btn.SendToChat(ctx)*/
                return nil

            case "terimaut":
                if !ctx.IsGroup {
                    return ctx.Reply("❌ Perintah ini hanya bisa digunakan di grup.")
                }

                src.SessionMutex.Lock()
                match, exists := pendingMatches[senderNumber]

                if !exists || match.GroupJID != groupJIDStr {
                    src.SessionMutex.Unlock()
                    return ctx.Reply("⚠️ Kamu tidak memiliki tantangan yang menunggu.")
                }

                delete(pendingMatches, senderNumber)
                challengerNumber := strings.Split(match.ChallengerJID, "@")[0]
                src.ActivePlayers[challengerNumber] = true
                src.ActivePlayers[senderNumber] = true
                src.SessionMutex.Unlock()
                
                ctx.Reply(fmt.Sprintf("✅ @%s menerima tantangan!\n\n⏳ Sedang membuat arena permainan...", senderNumber))

                res, err := src.GameWS.RequestCreateRoom(challengerNumber, senderNumber, groupJIDStr)
                if err != nil {
                    src.SessionMutex.Lock()
                    delete(src.ActivePlayers, challengerNumber)
                    delete(src.ActivePlayers, senderNumber)
                    src.SessionMutex.Unlock()
                    return ctx.Reply(fmt.Sprintf("❌ Gagal membuat room: %v", err))
                }

                gameURL := fmt.Sprintf("https://ps.azumi.dev/game?room=%s", res.RoomID)

                groupMsg := fmt.Sprintf("🎮 *ARENA ULAR TANGGA SIAP!*\n\n👥 @%s VS @%s\n\n🔑 Token telah dikirim ke PM masing-masing pemain.\n\n_Selamat bermain!_", challengerNumber, senderNumber)
                btn := ctx.Button()
                btn.SetTitle("🎲 `ULAR TANGGA`")
                btn.SetBody(groupMsg)
                btn.SetFooter("© Powered by Golang")
                btn.AddUrl("🌐 Buka Arena", gameURL, true)
                btn.AddMentions([]string{match.ChallengerJID, match.TargetJID})
                btn.SendToChat(ctx)

                sendPrivateToken(ctx, match.ChallengerJID, res.TokenA, gameURL)
                sendPrivateToken(ctx, match.TargetJID, res.TokenB, gameURL)
                return nil

            case "tolakut":
                src.SessionMutex.Lock()
                match, exists := pendingMatches[senderNumber]
                if !exists {
                    src.SessionMutex.Unlock()
                    return nil
                }

                delete(pendingMatches, senderNumber)
                src.SessionMutex.Unlock()
                

                challengerNumber := strings.Split(match.ChallengerJID, "@")[0]
                ctx.Print(senderNumber, challengerNumber)
                pesan := fmt.Sprintf("❌ @%s menolak tantangan dari @%s", senderNumber, challengerNumber)
                ctx.Reply(pesan)
                
                return nil

            case "stoput", "stopulartangga":
                src.SessionMutex.Lock()
                defer src.SessionMutex.Unlock()

                if src.ActivePlayers[senderNumber] {
                    delete(src.ActivePlayers, senderNumber)
                    return ctx.Reply("✅ Sesi permainan aktifmu berhasil dihapus!")
                }

                for target, match := range pendingMatches {
                    if match.ChallengerJID == senderJIDStr || match.TargetJID == senderJIDStr {
                        delete(pendingMatches, target)
                        return ctx.Reply("✅ Tantangan yang menggantung berhasil dibatalkan!")
                    }
                }

                return ctx.Reply("⚠️ Kamu tidak memiliki sesi permainan atau tantangan aktif.")
            }

            return nil
        },
    })
}

func sendPrivateToken(ctx *src.ContextBot, jidStr, token, url string) {
    parsedJID, err := types.ParseJID(jidStr)
    if err != nil {
        fmt.Printf("❌ Gagal parsing JID %s: %v\n", jidStr, err)
        return
    }

    btn := ctx.Button()
    btn.SetTitle("🔑 `TOKEN ULAR TANGGA`")
    btn.SetBody(fmt.Sprintf("\nMasukkan token ini *%s* di arena untuk memulai permainan.\n", token))
    btn.SetFooter("© Powered by Golang")

    btn.AddCopy("📋 Salin Token", token, "copy_btn")
    btn.AddUrl("🌐 Buka Arena", url, true)

    err = btn.Send(ctx, parsedJID)
    if err != nil {
        fmt.Printf("❌ Gagal mengirim token ke %s: %v\n", jidStr, err)
    }
}
