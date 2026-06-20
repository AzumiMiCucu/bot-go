//main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bot-go/commands"
	"bot-go/src"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

var client *whatsmeow.Client

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	
	case *events.Message:
		// [OPSIONAL TAPI PENTING UNTUK JAPRI] 
		// Subscribe ke pengirim agar bot bisa mendeteksi saat dia mengetik di kemudian waktu
		if !v.Info.IsGroup {
			client.SubscribePresence(context.Background(), v.Info.Chat)
		}

		// Proses async agar receive-loop whatsmeow tidak terblokir
		go MessageHandler(client, v)

	case *events.Connected:
		fmt.Println("[SYSTEM] ✅ Terhubung ke WhatsApp server.")
		
		// [TAMBAHAN WAJIB] Beritahu server WA bahwa bot ini ONLINE
		err := client.SendPresence(context.Background(), types.PresenceAvailable)
		if err != nil {
			fmt.Println("[SYSTEM] ⚠️ Gagal mengirim status online:", err)
		} else {
			fmt.Println("[SYSTEM] 🟢 Bot sekarang berstatus Online & siap mendeteksi typing.")
		}

	case *events.Disconnected:
		fmt.Println("[SYSTEM] ⚠️ Koneksi terputus. Menunggu rekoneksi...")

	case *events.ChatPresence:
		// Catat aktivitas "mengetik" untuk sinyal anti-bot (bot biasanya kirim
		// pesan TANPA composing). Hanya saat composing.
		if v.State == types.ChatPresenceComposing {
			commands.RecordTyping(v.MessageSource.Chat, v.MessageSource.Sender)
			fmt.Printf("[PRESENCE] ⌨️ composing dari %s di %s\n", v.MessageSource.Sender, v.MessageSource.Chat)
		}
	}
}

func main() {
	src.InitConfig()

	src.InitDatabase()
	

	fmt.Printf("[SYSTEM] 📚 Memuat %d modul perintah...\n", len(src.CommandRegistry))

	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:database/wa.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}

	clientLog := waLog.Stdout("Client", "INFO", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	if client.Store.ID == nil {
		err = client.Connect()
		if err != nil {
			panic(err)
		}

		nomorBot := src.AppConfig.BotNumber

		fmt.Printf("\n[AUTH] 🔐 Meminta Pairing Code untuk nomor: %s...\n", nomorBot)
		time.Sleep(3 * time.Second)

		code, err := client.PairPhone(context.Background(), nomorBot, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		if err != nil {
			fmt.Println("[ERROR] Gagal mendapatkan kode pairing:", err)
			return
		}

		formattedCode := code[:4] + code[4:]
		fmt.Printf("\n╔════════════════════════════════════════╗\n")
		fmt.Printf("║  🔑 KODE PAIRING ANDA: %s       ║\n", formattedCode)
		fmt.Printf("║  Buka WhatsApp > Perangkat Tertaut    ║\n")
		fmt.Print("╚════════════════════════════════════════╝\n\n")

	} else {
		err = client.Connect()
		if err != nil {
			panic(err)
		}
	}

	fmt.Println("[SYSTEM] ✨ Bot siap digunakan!")

	// Mulai scheduler latar belakang
	src.StartAutoReadScheduler(client)
	commands.StartReferralScheduler(client)
	// Pastikan package src sudah di-import
//src.InitGameWS(client)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	fmt.Println("\n[SYSTEM] 🛑 Mematikan layanan...")
	client.Disconnect()
	src.DB.Close()
}