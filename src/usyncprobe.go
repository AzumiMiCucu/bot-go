package src

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// =================================================================
// DETEKSI BOT PROAKTIF via USYNC — tanpa menunggu pesan masuk.
// =================================================================
// Berbeda dari engine struktural (botdetect.go / structdump.go) yang butuh
// SEBUAH pesan untuk dibedah, USync bisa MENANYA server WA tentang sebuah nomor
// kapan saja: IsOnWhatsApp (terdaftar? + verified_name) & GetUserDevices
// (daftar device). Semua ini network-query murni, tak perlu target mengirim apa
// pun.
//
// APA YANG BISA DIDETEKSI PROAKTIF:
//   • Akun BOT platform (server "bot"/"hosted", mis. Meta AI) → JID.IsBot()
//   • Akun BISNIS / WhatsApp Business API (punya verified_name) → sering = sender
//     otomatis/hosted
//   • Terdaftar / tidak
//   • Sidik device: jumlah device tertaut & ada-tidaknya device utama (index 0)
//
// BATAS JUJUR: USync TIDAK bisa membedakan bot library (Baileys/whatsmeow tak
// resmi) dari perangkat tertaut biasa — keduanya tampil sebagai device multi-
// device normal (tak ada verified_name, ada device 0). Untuk kasus itu tetap
// perlu engine struktural level-pesan. Jadi USync = PRA-SARING bisnis/bot
// platform, bukan pengganti botdetect.
// =================================================================

var reNonDigit = regexp.MustCompile(`\D`)

// ProbeResult = hasil probe USync satu nomor.
type ProbeResult struct {
	Input        string
	JID          types.JID
	Registered   bool
	IsBotJID     bool // server "bot"/"hosted" atau pola bot-user
	Business     bool // punya verified_name
	VerifiedName string
	DeviceCount  int
	HasPrimary   bool // ada device index 0 (HP utama)
	Devices      []types.JID
	Verdict      string // bot-platform / bisnis / personal / tidak-terdaftar
	Reason       string
}

// ProbeNumber menanyakan server WA tentang sebuah nomor/JID tanpa perlu pesan
// masuk. Input boleh "628xxxx", "628xxxx@s.whatsapp.net", atau JID mentah.
func ProbeNumber(ctx context.Context, client *whatsmeow.Client, input string) (ProbeResult, error) {
	res := ProbeResult{Input: input}
	if client == nil {
		return res, fmt.Errorf("client belum siap")
	}

	// Normalisasi → ambil digit nomor telepon.
	phone := input
	if strings.Contains(phone, "@") {
		phone = strings.SplitN(phone, "@", 2)[0]
	}
	phone = reNonDigit.ReplaceAllString(phone, "")
	if phone == "" {
		return res, fmt.Errorf("nomor tidak valid")
	}

	// 1) IsOnWhatsApp → terdaftar? + JID kanonik + verified_name (bisnis).
	onWA, err := client.IsOnWhatsApp(ctx, []string{phone})
	if err != nil {
		return res, err
	}
	if len(onWA) == 0 {
		res.Verdict = "tidak-terdaftar"
		res.Reason = "nomor tidak ada di WhatsApp"
		return res, nil
	}
	info := onWA[0]
	res.JID = info.JID
	res.Registered = info.IsIn
	if info.VerifiedName != nil && info.VerifiedName.Details != nil {
		res.Business = true
		res.VerifiedName = info.VerifiedName.Details.GetVerifiedName()
	}
	res.IsBotJID = info.JID.IsBot() || info.JID.Server == types.BotServer || info.JID.Server == types.HostedServer

	if !res.Registered {
		res.Verdict = "tidak-terdaftar"
		res.Reason = "nomor tidak terdaftar di WhatsApp"
		return res, nil
	}

	// 2) GetUserDevices → daftar device (jumlah + ada device utama).
	devs, err := client.GetUserDevices(ctx, []types.JID{res.JID.ToNonAD()})
	if err == nil {
		res.Devices = devs
		res.DeviceCount = len(devs)
		for _, d := range devs {
			if d.Device == 0 {
				res.HasPrimary = true
				break
			}
		}
	}

	// 3) Klasifikasi.
	switch {
	case res.IsBotJID:
		res.Verdict = "bot-platform"
		res.Reason = "JID di server bot/hosted (bot resmi WA/Meta)"
	case res.Business:
		res.Verdict = "bisnis"
		res.Reason = "punya verified_name (akun bisnis / kemungkinan WA Business API otomatis)"
	default:
		res.Verdict = "personal"
		res.Reason = "akun personal biasa — USync tak bisa memastikan bot library (Baileys) di level ini"
	}
	return res, nil
}
