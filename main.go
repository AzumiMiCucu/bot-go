// main.go
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
		// [HAPUS-VIA-REAKSI] Reaksi (emoji 🗑️/❌/🚮) dari owner/admin → hapus pesan.
		// Reaksi datang sebagai events.Message ber-ReactionMessage; tangani terpisah
		// karena tak punya teks/media dan akan di-drop oleh MessageHandler biasa.
		if v.Message != nil && v.Message.GetReactionMessage() != nil {
			go commands.HandleReactionDelete(client, v)
			return
		}

		// [OPSIONAL TAPI PENTING UNTUK JAPRI]
		// Subscribe ke pengirim agar bot bisa mendeteksi saat dia mengetik di kemudian waktu
		if !v.Info.IsGroup {
			// Jangan blokir receive-loop whatsmeow untuk network call presence.
			go client.SubscribePresence(context.Background(), v.Info.Chat)
		}

		// [CAPTURE] Dump story/status saluran yang disiarkan via status@broadcast.
		go src.CaptureStatusMessage(v)

		// Proses async agar receive-loop whatsmeow tidak terblokir
		go MessageHandler(client, v)

	case *events.NewsletterLiveUpdate:
		// [CAPTURE] Update/story saluran yang diikuti bot → dump struktur asli
		// ke tmp/capture_channel_status/*.json (untuk kalibrasi command upswc).
		go src.CaptureNewsletterLiveUpdate(v)

	case *events.FBMessage:
		// Pesan interop Meta (Messenger/Instagram ↔ WA) — sangat langka.
		// whatsmeow sudah mendekodenya; kita tangkap & ringkas (lihat src/armadillo.go).
		go src.HandleFBMessage(client, v)

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
		// JANGAN tutup panggilan di sini. events.Disconnected sering dipicu SESAAT
		// oleh whatsmeow (auto-reconnect / stream-replace), termasuk persis ketika
		// sesi panggilan baru sedang dibangun. Jalur media call berjalan di koneksi
		// UDP relay yang TERPISAH dari websocket WA, jadi blip websocket sesaat tak
		// seharusnya mematikan call — menutupnya di sini justru bikin panggilan
		// "baru mulai langsung mati". Pembersihan call hanya saat shutdown (lihat
		// akhir main) atau saat panggilan benar-benar di-terminate oleh server.

	case *events.ChatPresence:
		// Catat aktivitas "mengetik" untuk sinyal anti-bot (bot biasanya kirim
		// pesan TANPA composing). Hanya saat composing.
		if v.State == types.ChatPresenceComposing {
			commands.RecordTyping(v.MessageSource.Chat, v.MessageSource.Sender)
			//	fmt.Printf("[PRESENCE] ⌨️ composing dari %s di %s\n", v.MessageSource.Sender, v.MessageSource.Chat)
		}

	case *events.GroupInfo:
		// Sambutan / perpisahan grup (welcome & goodbye).
		if len(v.Join) > 0 {
			go commands.HandleGroupJoin(client, v)
		}
		if len(v.Leave) > 0 {
			go commands.HandleGroupLeave(client, v)
		}
	}
}

func main() {
	src.InitConfig()

	src.InitDatabase()
	if err := src.InitMessageStore(); err != nil {
		fmt.Printf("[DB] ⚠️ msg.db gagal diinisialisasi: %v\n", err)
	}
	if err := src.InitResponDB(); err != nil {
		fmt.Printf("[DB] ⚠️ respon.db gagal diinisialisasi: %v\n", err)
	}

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

	// Level WARN di produksi: DEBUG mencetak tiap node XMPP masuk/keluar
	// (I/O sinkron yang sangat berat & memperlambat respon di bawah beban).
	// Logger dibungkus NewCaptureLogger agar node <message> mentah (XMLString dari
	// sub-logger "Recv") tetap DITANGKAP untuk deteksi bot — tanpa harus DEBUG &
	// tanpa mencetak apa pun (lihat src/rawnode.go).
	clientLog := src.NewCaptureLogger(waLog.Stdout("Client", "WARN", true), "Client")
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	// Pasang stack panggilan (meowcaller) SEBELUM Connect() agar intersepsi
	// node <call>/<ack> level-rendah aktif sebelum receive-loop berjalan.
	src.InitCaller(client)

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
	src.StartAutoClearScheduler(client)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	fmt.Println("\n[SYSTEM] 🛑 Mematikan layanan...")
	src.HangupAllCalls()
	client.Disconnect()
	src.DB.Close()
	src.CloseMessageStore()
	src.CloseResponDB()
}
