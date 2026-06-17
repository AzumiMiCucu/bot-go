package src

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "sync"
    "time"
    "strings"

    "github.com/gorilla/websocket"
    "go.mau.fi/whatsmeow"
    "go.mau.fi/whatsmeow/types"
    waProto "go.mau.fi/whatsmeow/binary/proto"
    "google.golang.org/protobuf/proto"
)

// Konfigurasi Server Node.js
const WSServerURL = "wss://azumi.my.id/ws?type=bot&secret=kunci"

type WSManager struct {
    Conn       *websocket.Conn
    WAClient   *whatsmeow.Client
    mu         sync.Mutex
    PendingReq sync.Map
    MessageQueue []QueuedMessage
    queueMu      sync.Mutex
}

type QueuedMessage struct {
    GroupJID   string
    Message    string
    Mentions   []string
    Timestamp  time.Time
}

var GameWS *WSManager

type WSPayload struct {
    Action     string `json:"action,omitempty"`
    Event      string `json:"event,omitempty"`
    TagID      string `json:"tag_id,omitempty"`
    RoomID     string `json:"roomId,omitempty"`
    TokenA     string `json:"tokenA,omitempty"`
    TokenB     string `json:"tokenB,omitempty"`
    GroupJID   string `json:"group_jid,omitempty"`
    PlayerAJID string `json:"player_a_jid,omitempty"`
    PlayerBJID string `json:"player_b_jid,omitempty"`
    WinnerJID  string `json:"winner_jid,omitempty"`
    ScoreA     int    `json:"scoreA,omitempty"`
    ScoreB     int    `json:"scoreB,omitempty"`
    FinalPosA  int    `json:"finalPosA,omitempty"`
    FinalPosB  int    `json:"finalPosB,omitempty"`
}

func InitGameWS(waClient *whatsmeow.Client) {
    GameWS = &WSManager{
        WAClient: waClient,
        MessageQueue: make([]QueuedMessage, 0),
    }
    go GameWS.Connect()
    go GameWS.ProcessQueue()
}

func (w *WSManager) ProcessQueue() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        w.queueMu.Lock()
        if len(w.MessageQueue) == 0 {
            w.queueMu.Unlock()
            continue
        }

        // Check if WA client is connected
        if w.WAClient == nil || !w.WAClient.IsConnected() {
            w.queueMu.Unlock()
            continue
        }

        // Process messages
        var failedMessages []QueuedMessage
        for _, qMsg := range w.MessageQueue {
            // Skip messages older than 10 minutes
            if time.Since(qMsg.Timestamp) > 10*time.Minute {
                log.Printf("⚠️ Pesan queue terlalu lama, dibuang: %s", qMsg.GroupJID)
                continue
            }

            groupJID, err := types.ParseJID(qMsg.GroupJID)
            if err != nil {
                log.Printf("❌ Error parsing queued group JID: %v", err)
                continue
            }

            _, err = w.WAClient.SendMessage(context.Background(), groupJID, &waProto.Message{
                ExtendedTextMessage: &waProto.ExtendedTextMessage{
                    Text: proto.String(qMsg.Message),
                    ContextInfo: &waProto.ContextInfo{
                        MentionedJID: qMsg.Mentions,
                    },
                },
            })

            if err != nil {
                log.Printf("❌ Gagal kirim pesan dari queue: %v", err)
                failedMessages = append(failedMessages, qMsg)
            } else {
                log.Printf("✅ Berhasil kirim pesan dari queue ke %s", qMsg.GroupJID)
            }
        }

        // Update queue with failed messages only
        w.MessageQueue = failedMessages
        w.queueMu.Unlock()
    }
}

func (w *WSManager) Connect() {
    for {
        fmt.Println("🔄 Mencoba terhubung ke Game WS Server...")

        header := map[string][]string{
            "Origin":     {"https://azumi.my.id"},
            "User-Agent": {"Mozilla/5.0"},
        }

        dialer := websocket.Dialer{
            HandshakeTimeout: 10 * time.Second,
        }

        conn, resp, err := dialer.Dial(WSServerURL, header)

        if err != nil {
            if resp != nil {
                fmt.Println("HTTP Status:", resp.Status)
            }
            fmt.Println("❌ Gagal terhubung ke WS:", err)
            time.Sleep(5 * time.Second)
            continue
        }

        fmt.Println("✅ Bot berhasil terhubung ke Game WS Server!")

        w.mu.Lock()
        w.Conn = conn
        w.mu.Unlock()

        w.ReadLoop()

        w.mu.Lock()
        w.Conn.Close()
        w.Conn = nil
        w.mu.Unlock()
    }
}

func (w *WSManager) ReadLoop() {
    for {
        _, message, err := w.Conn.ReadMessage()
        if err != nil {
            log.Println("⚠️ WS Game Terputus:", err)
            break
        }

        var payload WSPayload
        if err := json.Unmarshal(message, &payload); err != nil {
            continue
        }

        if payload.Event == "room_created" {
            if ch, ok := w.PendingReq.Load(payload.TagID); ok {
                ch.(chan WSPayload) <- payload
                w.PendingReq.Delete(payload.TagID)
            }
        }

        if payload.Event == "game_over" {
            w.handleGameOver(payload)
        }

        if payload.Event == "room_expired" {
            w.handleRoomExpired(payload)
        }
    }
}

func (w *WSManager) RequestCreateRoom(playerA, playerB, groupJID string) (WSPayload, error) {
    w.mu.Lock()
    conn := w.Conn
    w.mu.Unlock()

    if conn == nil {
        return WSPayload{}, fmt.Errorf("server game sedang offline")
    }

    tagID := GenerateAndroidMessageID()
    req := WSPayload{
        Action:     "create_room",
        TagID:      tagID,
        GroupJID:   groupJID,
        PlayerAJID: playerA,
        PlayerBJID: playerB,
    }

    ch := make(chan WSPayload)
    w.PendingReq.Store(tagID, ch)

    err := conn.WriteJSON(req)
    if err != nil {
        w.PendingReq.Delete(tagID)
        return WSPayload{}, err
    }

    select {
    case res := <-ch:
        return res, nil
    case <-time.After(5 * time.Second):
        w.PendingReq.Delete(tagID)
        return WSPayload{}, fmt.Errorf("timeout menunggu server game")
    }
}

func (w *WSManager) handleGameOver(payload WSPayload) {
    winnerJID := payload.WinnerJID
    winnerNumber := strings.Split(payload.WinnerJID, "@")[0]
    playerANumber := strings.Split(payload.PlayerAJID, "@")[0]
    playerBNumber := strings.Split(payload.PlayerBJID, "@")[0]

    // Build message with scores
    msg := fmt.Sprintf(
        "🎉 *GAME ULAR TANGGA SELESAI!*\n\n"+
        "🏆 *PEMENANG:* @%s\n\n"+
        "📊 *SKOR AKHIR:*\n"+
        "├ Player A (@%s): %d poin\n"+
        "└ Player B (@%s): %d poin\n\n"+
        "🎮 Posisi Akhir: %d vs %d\n\n"+
        "_Sesi permainan telah ditutup._",
        winnerNumber,
        playerANumber, payload.ScoreA,
        playerBNumber, payload.ScoreB,
        payload.FinalPosA, payload.FinalPosB,
    )

    mentions := []string{winnerJID+"@lid", payload.PlayerAJID+"@lid", payload.PlayerBJID+"@lid"}

    // Check if WA client is connected
    if w.WAClient == nil || !w.WAClient.IsConnected() {
        log.Printf("⚠️ WA Client tidak terhubung, menyimpan pesan ke queue")
        w.queueMu.Lock()
        w.MessageQueue = append(w.MessageQueue, QueuedMessage{
            GroupJID:  payload.GroupJID,
            Message:   msg,
            Mentions:  mentions,
            Timestamp: time.Now(),
        })
        w.queueMu.Unlock()
        ClearPlayerSession(payload.PlayerAJID, payload.PlayerBJID)
        return
    }

    groupJID, err := types.ParseJID(payload.GroupJID)
    if err != nil {
        log.Printf("❌ Error parsing group JID: %v", err)
        ClearPlayerSession(payload.PlayerAJID, payload.PlayerBJID)
        return
    }

    _, err = w.WAClient.SendMessage(context.Background(), groupJID, &waProto.Message{
        ExtendedTextMessage: &waProto.ExtendedTextMessage{
            Text: proto.String(msg),
            ContextInfo: &waProto.ContextInfo{
                MentionedJID: mentions,
            },
        },
    })

    if err != nil {
        log.Printf("❌ Gagal kirim pesan game over: %v, menyimpan ke queue", err)
        // Save to queue if send failed
        w.queueMu.Lock()
        w.MessageQueue = append(w.MessageQueue, QueuedMessage{
            GroupJID:  payload.GroupJID,
            Message:   msg,
            Mentions:  mentions,
            Timestamp: time.Now(),
        })
        w.queueMu.Unlock()
    } else {
        log.Printf("✅ Berhasil kirim pesan game over ke %s", payload.GroupJID)
    }

    // Hapus sesi player dari activePlayers
    ClearPlayerSession(payload.PlayerAJID, payload.PlayerBJID)
}

func (w *WSManager) handleRoomExpired(payload WSPayload) {
    groupJID, err := types.ParseJID(payload.GroupJID)
    if err != nil {
        return
    }

    msg := "⏰ *SESI ULAR TANGGA BERAKHIR*\n\nPermainan telah ditutup karena tidak ada aktivitas dalam 15 menit."

    w.WAClient.SendMessage(context.Background(), groupJID, &waProto.Message{
        Conversation: proto.String(msg),
    })
}
