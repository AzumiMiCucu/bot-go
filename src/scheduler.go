package src

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// AUTO-READ: tandai pesan grup sebagai dibaca secara batch tiap 30 menit
// agar notifikasi tidak menumpuk saat WhatsApp dibuka.
// =================================================================

type pendingRead struct {
	Chat      types.JID
	Sender    types.JID
	IDs       []types.MessageID
	Timestamp time.Time
}

var (
	pendingReads   = make(map[string]*pendingRead) // key = chat|sender
	pendingReadsMu sync.Mutex
)

// AddPendingRead menampung pesan grup yang masuk untuk ditandai-dibaca nanti.
// Dipanggil dari handler tiap kali ada pesan grup.
func AddPendingRead(chat, sender types.JID, id types.MessageID, ts time.Time) {
	key := chat.String() + "|" + sender.String()

	pendingReadsMu.Lock()
	defer pendingReadsMu.Unlock()

	pr, ok := pendingReads[key]
	if !ok {
		pr = &pendingRead{Chat: chat, Sender: sender}
		pendingReads[key] = pr
	}
	pr.IDs = append(pr.IDs, id)
	if ts.After(pr.Timestamp) {
		pr.Timestamp = ts
	}
	// Batasi memori: simpan maksimal 100 ID terakhir per pengirim
	if len(pr.IDs) > 100 {
		pr.IDs = pr.IDs[len(pr.IDs)-100:]
	}
}

// StartAutoReadScheduler menjalankan flush auto-read tiap 30 menit.
func StartAutoReadScheduler(client *whatsmeow.Client) {
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			flushPendingReads(client)
		}
	}()
	fmt.Println("[SCHEDULER] ✅ Auto-read grup aktif (tiap 30 menit).")
}

func flushPendingReads(client *whatsmeow.Client) {
	pendingReadsMu.Lock()
	batch := pendingReads
	pendingReads = make(map[string]*pendingRead)
	pendingReadsMu.Unlock()

	if len(batch) == 0 {
		return
	}

	count := 0
	for _, pr := range batch {
		if len(pr.IDs) == 0 {
			continue
		}
		err := client.MarkRead(context.Background(), pr.IDs, pr.Timestamp, pr.Chat, pr.Sender)
		if err == nil {
			count += len(pr.IDs)
		}
	}
	if count > 0 {
		fmt.Printf("[SCHEDULER] 📖 Auto-read: %d pesan ditandai dibaca.\n", count)
	}
}
