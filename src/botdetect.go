package src

import (
	"regexp"
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// =================================================================
// BOT / BAILEYS / DEVICE DETECTION UTILITIES
// =================================================================

// Baileys ID prefixes — library WhatsApp non-resmi (Baileys) punya format
// message-ID yang TUMPANG-TINDIH dengan client resmi, jadi harus dipisah:
//
//	BAE* (BAE5, BAE0..) — SIDIK JARI BAILEYS. Client WA resmi TAK PERNAH memakai
//	                      prefix ini → bukti Baileys yang KUAT & berdiri sendiri.
//	3EB0               — AMBIGU: dipakai BERSAMA oleh WA Web/Desktop RESMI dan
//	                      Baileys lama. ID 3EB0 SENDIRIAN tak membuktikan apa pun;
//	                      harus dikuatkan absennya metadata multi-device (isUseDevice)
//	                      & messageSecret. Inilah pembeda "bot beneran vs WA Web".
var (
	reBaileysStrong = regexp.MustCompile(`^BAE[0-9A-F]{2}`) // keluarga BAE* — Baileys pasti
	reBaileysWeb    = regexp.MustCompile(`^3EB0`)           // 3EB0 — ambigu (WA Web resmi ATAU Baileys lama)
)

// Verdict: kesimpulan akhir deteksi. Satu sumber kebenaran yang dipakai cekbot
// (tampilan) dan antibot (scoring). Pembeda utama bot vs manusia adalah
// DeviceListMetadata — app WhatsApp resmi SELALU menyertakannya di dalam
// MessageContextInfo; library client (Baileys/whatsmeow) hampir selalu tidak.
const (
	VerdictHuman   = "human"   // app WA resmi (Android/iOS/Web/Desktop)
	VerdictBot     = "bot"     // bot resmi WA / Meta / business HOSTED (non-E2EE)
	VerdictBaileys = "baileys" // library client (Baileys/whatsmeow) tak resmi
	VerdictSuspect = "suspect" // sinyal lemah/campur — butuh konfirmasi perilaku
	VerdictUnknown = "unknown" // metadata raw tak tersedia (jalur quote/terbatas)
)

// BotDetectionResult menyimpan seluruh sinyal deteksi bot/Baileys.
type BotDetectionResult struct {
	MessageID  string
	SenderUser string // nomor pengirim (user dari JID)
	SenderFull string // JID lengkap pengirim
	PushName   string

	// Baileys signals
	IsBaileysID     bool   // KESIMPULAN akhir: true bila pengirim memakai Baileys
	BaileysIDPrefix string // prefix yang cocok (BAE5 / 3EB0 / dsb)
	StrongBaileys   bool   // true bila ID dari keluarga BAE* (Baileys pasti, WA resmi tak pakai)
	AmbiguousID     bool   // true bila ID 3EB0 (bisa WA Web resmi ATAU Baileys lama)
	BaileysScore    int    // skor keyakinan Baileys (0-25)

	// Display / engine fields (gaya "ISBOT CHECKER")
	IDLength    int    // panjang message-ID
	MessageType string // nama tipe pesan proto (extendedTextMessage, conversation, dll)
	Platform    string // platform dari format ID (web/android/ios/desktop/unknown) — alias DeviceClass
	IsUseDevice bool   // true bila pesan membawa metadata multi-device ASLI (deviceListMetadata/messageSecret)

	// Hasil 4 sub-pemeriksaan engine: "OK" bila lolos, atau penanda masalah singkat.
	TKCheck string // Token  — envelope/messageContextInfo (token e2ee) utuh
	DVCheck string // Device — status device (primary/secondary) & index teridentifikasi
	PFCheck string // Platform — platform terdeteksi dari format ID
	IDCheck string // ID — format/panjang ID dikenali (vs sidik jari Baileys)

	// Device signals
	DeviceClass     string // ios / web / android / desktop / unknown (dari ClassifyDeviceFromID)
	IsUnknownDevice bool   // true bila DeviceClass == unknown
	AddressingMode  string // pn / lid — HANYA informasional (lid bukan sinyal bot, dipakai semua HP modern)
	IsMultiDevice   bool   // true bila ada DeviceListMetadata atau DeviceSentMeta
	IsGroup         bool   // true bila pesan datang dari GRUP. PENTING: pesan teks grup
	// dienkripsi Sender Key (skmsg) sehingga TAK membawa DeviceListMetadata per-pesan —
	// jadi di grup, absennya metadata multi-device BUKAN sinyal bot (berlaku untuk HP
	// utama MAUPUN perangkat tertaut/companion resmi).

	// Device-index dari JID (sumber primary/secondary yang BENAR — bukan pn/lid).
	// Device 0 = HP UTAMA (primary): registrasi nomor langsung; Baileys/whatsmeow
	// TAK BISA jadi device 0 (mereka pairing sebagai companion lewat QR/kode).
	// Device != 0 = perangkat COMPANION: WA Web, Desktop, DAN bot library.
	DeviceID    uint16 // indeks device dari JID pengirim
	IsPrimary   bool   // true bila DeviceID == 0 (HP utama)
	IsSecondary bool   // true bila DeviceID != 0 (perangkat companion)

	// Bot signals
	IsBot           bool   // true bila terdeteksi sebagai bot
	HasBotMetadata  bool   // true bila MessageContextInfo.BotMetadata != nil
	HasBotSecret    bool   // true bila ada botMessageSecret
	HasDeviceListMD bool   // true bila DeviceListMetadata ada di raw message
	IsBotInvoke     bool   // true bila pesan adalah BotInvoke
	IsFromBotServer bool   // true bila sender.Server == "bot" atau chat.Server == "bot"
	SenderServer    string // server type dari JID sender

	// Multi-device metadata
	SenderAccountType  int32 // ADVEncryptionType dari DeviceListMetadata (0=E2EE, 1=HOSTED)
	IsHostedEncryption bool  // true bila SenderAccountType == HOSTED
	HasMessageSecret   bool  // true bila ada MessageSecret (hampir semua pesan WA modern)

	// Raw diagnostics
	RawMessageContextInfo bool // true bila pesan mentah punya MessageContextInfo
	EditType              string

	// Kesimpulan akhir (lihat konstanta Verdict*)
	Verdict       string // human / bot / baileys / suspect / unknown
	VerdictReason string // alasan singkat verdict (untuk tampilan & log)

	// DefinitiveBot true bila verdict bot/baileys berasal dari sinyal DEFINITIF
	// (server-bot / BAE* / HOSTED / interaktif / android-reply-tanpa-quotedType /
	// reputasi akun) — BUKAN dari fallback lemah "format ID tak dikenal". Hanya yang
	// definitif yang boleh MENANDAI reputasi akun (lihat botrep.go) agar tak meracuni.
	DefinitiveBot bool

	// Sidik jari STRUKTURAL (level protobuf) — lihat structdump.go. Dipakai cekbot,
	// command `struktur`, dan log antibot untuk membandingkan bot vs HP asli.
	Struct StructReport

	// Sidik jari NODE XMPP MENTAH (<message ...> + child & atribut) — lihat rawnode.go.
	// Ditangkap dari logger "Recv" saat pesan tiba live; hilang pada jalur quote.
	RawNode RawNodeReport
}

// DetectBot menganalisis pesan dari semua sudut untuk mendeteksi bot/Baileys.
// Ini adalah fungsi SERBA-GUNA yang dipakai oleh cekbot (diagnostik) dan
// antibot (scoring). Mengembalikan struct komprehensif berisi semua sinyal.
func DetectBot(evt *events.Message) BotDetectionResult {
	r := BotDetectionResult{
		MessageID:  string(evt.Info.ID),
		SenderFull: evt.Info.Sender.String(),
		SenderUser: evt.Info.Sender.ToNonAD().User,
		PushName:   evt.Info.PushName,
		IsGroup:    evt.Info.IsGroup,
	}

	// ── 1. Classify message-ID (Baileys + device type) ──
	r.DeviceClass = ClassifyDeviceFromID(r.MessageID)
	r.Platform = r.DeviceClass
	r.IsUnknownDevice = r.DeviceClass == "unknown"
	r.IDLength = len(r.MessageID)
	r.MessageType = messageTypeName(evt.Message)

	// Pisah sidik jari Baileys: BAE* = pasti; 3EB0 = ambigu (dikuatkan nanti).
	r.StrongBaileys, r.AmbiguousID, r.BaileysIDPrefix = classifyBaileysID(r.MessageID)

	// ── 2. Device addressing (informasional) + device-index (sinyal primary/secondary) ──
	// Di grup utamakan SenderAlt (alamat asli) bila ada; selain itu Sender.
	addrJID := evt.Info.Sender
	if evt.Info.IsGroup && !evt.Info.MessageSource.SenderAlt.IsEmpty() {
		addrJID = evt.Info.MessageSource.SenderAlt
	}
	classifyAddressing(&r, addrJID.String())
	classifyDevice(&r, addrJID.Device)

	// ── 3. DeviceListMetadata & DeviceSentMeta ──
	if evt.Info.DeviceSentMeta != nil {
		r.IsMultiDevice = true
	}

	// ── 4. Raw message analysis ──
	if evt.RawMessage != nil {
		raw := evt.RawMessage

		// MessageContextInfo
		mci := raw.GetMessageContextInfo()
		r.RawMessageContextInfo = mci != nil
		if mci != nil {
			r.HasMessageSecret = len(mci.GetMessageSecret()) > 0
			r.HasBotSecret = len(mci.GetBotMessageSecret()) > 0
			r.HasBotMetadata = mci.GetBotMetadata() != nil

			if dlm := mci.GetDeviceListMetadata(); dlm != nil {
				r.HasDeviceListMD = true
				r.IsMultiDevice = true
				if dlm.SenderAccountType != nil {
					r.SenderAccountType = int32(*dlm.SenderAccountType)
					if r.SenderAccountType == 1 { // HOSTED
						r.IsHostedEncryption = true
					}
				}
			}
		}

		// BotInvoke wrapper
		if raw.GetBotInvokeMessage().GetMessage() != nil {
			r.IsBotInvoke = true
		}
	}

	// ── 5. Bot server detection ──
	r.SenderServer = evt.Info.Sender.Server
	r.IsFromBotServer = evt.Info.Sender.Server == "bot" ||
		evt.Info.Chat.Server == "bot" ||
		strings.HasPrefix(r.SenderFull, "0@") ||
		strings.HasSuffix(r.SenderFull, "@bot")

	// ── 5b. isUseDevice — metadata multi-device ASLI (pembeda WA Web vs Baileys) ──
	// WA Web/Desktop RESMI selalu mengikat envelope multi-device (deviceListMetadata
	// dan/atau messageSecret pada messageContextInfo). Baileys lama yang memakai ID
	// 3EB0 yang sama UMUMNYA tak menyertakannya. Inilah sinyal yang membuat ID 3EB0
	// ambigu bisa dipisahkan: ada metadata device → WA Web; tak ada → Baileys.
	r.IsUseDevice = r.HasDeviceListMD || r.IsMultiDevice || r.HasMessageSecret

	// ── 5c. Finalisasi isBaileys ──
	// BAE* → Baileys pasti. 3EB0 → Baileys HANYA bila tak ada metadata device asli
	// (companion tanpa isUseDevice). Dengan begitu WA Web resmi (3EB0 + isUseDevice)
	// TIDAK lagi salah divonis Baileys seperti sebelumnya.
	//
	// PENTING (perbaikan false-positive perangkat tertaut): di GRUP, pesan teks
	// dienkripsi Sender Key (skmsg) sehingga TAK pernah membawa deviceListMetadata/
	// messageSecret per-pesan — jadi isUseDevice WAJAR bernilai false di grup, baik
	// dari HP utama maupun perangkat tertaut RESMI (WA Web/Desktop/HP lain). Karena
	// itu "3EB0 + tanpa isUseDevice" TIDAK boleh dianggap Baileys saat di grup; hanya
	// sidik jari KUAT (BAE*) yang berlaku di grup.
	r.IsBaileysID = r.StrongBaileys || (r.AmbiguousID && !r.IsUseDevice && !r.IsGroup)

	// ── 5d. Engine checks (TK/DV/PF/ID) ──
	computeEngineChecks(&r, true)

	// ── 6. Composite bot determination ──
	// IsBot = akun bot sungguhan (server-level). botMetadata/BotInvoke TIDAK dihitung
	// di sini — itu sinyal thread-bot (interaksi), bukan bukti pengirim adalah bot.
	r.IsBot = r.IsFromBotServer

	// ── 7. Baileys confidence score ──
	// Formula: akumulasi sinyal yang berkorelasi dengan Baileys.
	// >=10 → kemungkinan besar Baileys.
	r.BaileysScore = computeBaileysScore(&r)

	// ── 8. Edit type info ──
	if evt.Info.Edit != "" {
		r.EditType = string(evt.Info.Edit)
	}

	// ── 9. Sidik jari struktural (protobuf mentah, sebelum unwrap) ──
	r.Struct = AnalyzeStructure(evt.RawMessage)

	// ── 9b. Sidik jari node XMPP mentah (ditangkap live dari logger "Recv") ──
	// Recv-goroutine menyimpan node SEBELUM event di-dispatch ke handler ini, jadi
	// saat DetectBot berjalan node mestinya sudah ada di cache (dikunci message-ID).
	if rn, ok := LookupRawNode(r.MessageID); ok {
		r.RawNode = rn
		// Sinyal node yang AMAN dipakai (hindari false-positive — lihat filosofi
		// structdump.go): kehadiran child <biz>/<verified_name> = akun business,
		// child <bot> = thread bot. Hanya menguatkan flag yang sudah ada; aturan
		// vonis baru berbasis node menanti kalibrasi dari dump nyata (cmd struktur).
		if rn.Has("biz") || rn.Has("verified_name") {
			r.HasBotMetadata = true
		}
		if rn.Has("bot") {
			r.IsBotInvoke = true
		}
	}

	// ── 10. Kesimpulan akhir ──
	classifyVerdict(&r, true)

	// ── 11. Naikkan verdict bila AKUN sudah dikenal bot dari pesan sebelumnya
	//        (memori antar-pesan — menangkap bot web/iOS yang kirim pesan biasa). ──
	applyAccountReputation(&r)

	return r
}

// classifyAddressing mengisi AddressingMode (HANYA informasional). pn/lid BUKAN
// pembeda primary/secondary — WhatsApp modern memakai lid untuk semua perangkat
// termasuk HP utama. Primary/secondary ditentukan classifyDevice (device-index).
func classifyAddressing(r *BotDetectionResult, addrJID string) {
	if strings.Contains(addrJID, "@lid") {
		r.AddressingMode = "lid"
	} else {
		r.AddressingMode = "pn"
	}
}

// classifyDevice menetapkan primary/secondary dari device-index JID. Device 0 =
// HP utama (registrasi nomor langsung — Baileys/whatsmeow tak bisa jadi device 0);
// device != 0 = perangkat companion (WA Web/Desktop/bot library).
func classifyDevice(r *BotDetectionResult, device uint16) {
	r.DeviceID = device
	if device == 0 {
		r.IsPrimary = true
	} else {
		r.IsSecondary = true
	}
}

// extractUserJID mengambil bagian user (sebelum "@") dari sebuah JID string.
func extractUserJID(jid string) string {
	if jid == "" {
		return "unknown"
	}
	if i := strings.IndexByte(jid, '@'); i >= 0 {
		return jid[:i]
	}
	return jid
}

// serverOf mengambil bagian server (setelah "@") dari sebuah JID string.
func serverOf(jid string) string {
	if i := strings.IndexByte(jid, '@'); i >= 0 {
		return jid[i+1:]
	}
	return ""
}

// DetectBotFromQuoted menganalisis pesan yang DI-REPLY (di-quote). Kita hanya punya
// StanzaID (message-ID asli) + participant (JID pengirim asli), plus — bila tersedia —
// isi pesan yang di-quote (quoted) yang KADANG masih membawa sebagian metadata mentah
// (MessageContextInfo: messageSecret/deviceListMetadata/botMetadata, atau BotInvoke).
// Ini menggantikan analisis parsial lama di command cekbot (satu sumber kebenaran).
func DetectBotFromQuoted(stanzaID, participant string, quoted *waProto.Message) BotDetectionResult {
	r := BotDetectionResult{
		MessageID:  stanzaID,
		SenderFull: participant,
		SenderUser: extractUserJID(participant),
	}

	// ── ID & device class ──
	r.DeviceClass = ClassifyDeviceFromID(stanzaID)
	r.Platform = r.DeviceClass
	r.IsUnknownDevice = r.DeviceClass == "unknown"
	r.IDLength = len(stanzaID)
	r.MessageType = messageTypeName(quoted)
	r.StrongBaileys, r.AmbiguousID, r.BaileysIDPrefix = classifyBaileysID(stanzaID)

	// ── Addressing & server ──
	classifyAddressing(&r, participant)
	if pj, err := types.ParseJID(participant); err == nil {
		classifyDevice(&r, pj.Device)
		// Normalisasi key reputasi (buang device-suffix) agar cocok dgn jalur live.
		if u := pj.ToNonAD().User; u != "" {
			r.SenderUser = u
		}
	}
	r.SenderServer = serverOf(participant)
	r.IsFromBotServer = r.SenderServer == "bot" ||
		strings.HasSuffix(participant, "@bot") ||
		strings.HasPrefix(participant, "0@")

	// ── Raw metadata dari pesan yang di-quote (bila tersisa) ──
	if quoted != nil {
		if mci := quoted.GetMessageContextInfo(); mci != nil {
			r.RawMessageContextInfo = true
			r.HasMessageSecret = len(mci.GetMessageSecret()) > 0
			r.HasBotSecret = len(mci.GetBotMessageSecret()) > 0
			r.HasBotMetadata = mci.GetBotMetadata() != nil
			if dlm := mci.GetDeviceListMetadata(); dlm != nil {
				r.HasDeviceListMD = true
				r.IsMultiDevice = true
				if dlm.SenderAccountType != nil {
					r.SenderAccountType = int32(*dlm.SenderAccountType)
					r.IsHostedEncryption = r.SenderAccountType == 1
				}
			}
		}
		if quoted.GetBotInvokeMessage().GetMessage() != nil {
			r.IsBotInvoke = true
		}
	}

	// isUseDevice & finalisasi isBaileys (sama seperti jalur live, lihat DetectBot).
	r.IsUseDevice = r.HasDeviceListMD || r.IsMultiDevice || r.HasMessageSecret
	r.IsBaileysID = r.StrongBaileys || (r.AmbiguousID && !r.IsUseDevice)
	computeEngineChecks(&r, r.RawMessageContextInfo)

	// Sidik jari struktural dari salinan quoted (terbatas — WA membuang sebagian
	// metadata raw dari pesan yang di-reply, jadi hasilnya kurang lengkap dari live).
	r.Struct = AnalyzeStructure(quoted)

	// IsBot = akun bot sungguhan (server-level). botMetadata/BotInvoke TIDAK dihitung
	// di sini — itu sinyal thread-bot (interaksi), bukan bukti pengirim adalah bot.
	r.IsBot = r.IsFromBotServer
	r.BaileysScore = computeBaileysScore(&r)

	// Jalur quote: metadata raw (DeviceListMetadata) biasanya SUDAH DIBUANG WA dari
	// salinan pesan yang di-reply, jadi kita tak bisa memakai sinyal "tanpa DLM".
	// Tandai sebagai metadata tak lengkap → verdict cenderung "unknown" bila tak ada
	// sinyal definitif (bot server / Baileys ID / HOSTED).
	classifyVerdict(&r, r.RawMessageContextInfo)
	// Reputasi akun berlaku lintas-jalur: bila akun ini sudah dikenal bot dari pesan
	// live sebelumnya, salinan quote-nya pun divonis bot (metadata quote tak perlu).
	applyAccountReputation(&r)
	return r
}

// classifyVerdict menetapkan kesimpulan akhir. liveEnvelope=true berarti hasil ini
// berasal dari pesan yang ditangkap LIVE (metadata raw — termasuk DeviceListMetadata —
// masih utuh). Pada jalur quote (liveEnvelope=false) metadata DLM sudah dibuang WA,
// jadi sinyal "tanpa DLM" TIDAK bisa dipercaya dan verdict cenderung unknown/suspect.
func classifyVerdict(r *BotDetectionResult, liveEnvelope bool) {
	// 1. Akun bot SUNGGUHAN (level server): @bot / 0@ / server "bot". Ini satu-satunya
	//    sinyal "sender memang bot" yang andal dari metadata. CATATAN: botMetadata /
	//    botMessageSecret / BotInvoke TIDAK lagi dipakai di sini — itu menandai pesan
	//    BERADA DI THREAD BOT (mis. manusia me-reply Meta AI / memanggil command), BUKAN
	//    bukti PENGIRIM adalah bot. Dulu inilah penyebab HP Android asli divonis BOT saat
	//    membalas/memakai bot. Sinyal itu kini hanya jadi SUSPECT pada companion (lihat #6).
	if r.IsFromBotServer {
		r.Verdict = VerdictBot
		r.VerdictReason = "akun/server bot WhatsApp"
		r.DefinitiveBot = true
		return
	}

	// 2. Sidik jari Baileys KUAT (keluarga BAE*) — WA resmi tak pernah memakainya.
	//    Ini menang atas metadata device apa pun (Baileys bisa memalsukan envelope).
	if r.StrongBaileys {
		r.Verdict = VerdictBaileys
		r.VerdictReason = "ID Baileys (" + r.BaileysIDPrefix + ")"
		r.DefinitiveBot = true
		return
	}

	// 3. Enkripsi HOSTED (non-E2EE) — client business/cloud API, bukan app personal.
	if r.IsHostedEncryption {
		r.Verdict = VerdictBot
		r.VerdictReason = "enkripsi HOSTED (non-E2EE)"
		r.DefinitiveBot = true
		return
	}

	// 4. BUKTI POSITIF MANUSIA — HP UTAMA (device-index 0). Baileys/whatsmeow TAK BISA
	//    jadi device 0 (mereka companion). Maka device-0 = manusia, dan ini MENANG atas
	//    metadata bot-thread (botSecret/BotInvoke pada device 0 = manusia berinteraksi
	//    dgn bot). Hanya berlaku pada pesan LIVE — di jalur quote device-index tak andal.
	if liveEnvelope && r.IsPrimary {
		r.Verdict = VerdictHuman
		r.VerdictReason = "HP utama (device 0) — E2EE"
		return
	}

	// 4b. TIPE PESAN INTERAKTIF (tombol/list/template/interactive) — TIDAK BISA dibuat
	//     user WhatsApp personal; hanya bot/business-API. Sinyal bot yang berlaku TANPA
	//     perlu reply. Menang atas DeviceListMetadata (bot Theresav memalsukan DLM tapi
	//     terbongkar oleh interactiveMessage). Ditempatkan setelah #4 agar tak menyentuh
	//     HP utama (device 0) — bila kelak ada akun business resmi di HP sendiri.
	if liveEnvelope && r.Struct.InteractiveCompose {
		r.Verdict = VerdictBot
		r.VerdictReason = "pesan interaktif (tombol/list) — hanya bot/business"
		r.DefinitiveBot = true
		return
	}

	// 5. BUKTI POSITIF MANUSIA: DeviceListMetadata ADA → app WA asli (E2EE pairwise).
	//    PENTING — bukti SATU ARAH saja: ada DLM = manusia, tapi ABSENNYA DLM BUKAN
	//    sinyal bot. Pesan di GRUP dienkripsi dengan Sender Key (skmsg), sehingga
	//    pesan teks grup dari app resmi pun NORMAL tanpa DeviceListMetadata (DLM cuma
	//    ikut di SenderKeyDistributionMessage berkala, bukan tiap pesan). Menganggap
	//    "tanpa DLM = bot" membuat HP Android asli di grup divonis bot (false positive).
	if liveEnvelope && r.HasDeviceListMD {
		r.Verdict = VerdictHuman
		r.VerdictReason = "DeviceListMetadata + E2EE (app WA asli)"
		return
	}

	// 5b. ID 3EB0 AMBIGU yang TAK didukung metadata device asli (tanpa isUseDevice).
	//     CATATAN: r.IsBaileysID untuk 3EB0 kini HANYA true di luar grup (lihat 5c di
	//     DetectBot) — sebab di grup absennya metadata adalah hal NORMAL (skmsg), bukan
	//     sinyal Baileys. Jadi cabang ini menangkap Baileys lama ber-3EB0 pada chat
	//     PRIBADI, sementara perangkat tertaut resmi (WA Web 3EB0) lolos sebagai manusia
	//     lewat #4/#5/#6.
	if liveEnvelope && r.IsBaileysID && r.AmbiguousID && !r.IsUseDevice {
		r.Verdict = VerdictBaileys
		r.VerdictReason = "ID 3EB0 tanpa metadata multi-device (Baileys)"
		r.DefinitiveBot = true
		return
	}

	// 5c. JEJAK STRUKTURAL BAILEYS — "ngaku Android tapi reply tanpa quotedType".
	//     Klien WhatsApp ASLI selalu menempelkan field ContextInfo.quotedType (enum,
	//     field 71) saat me-reply; Baileys/whatsmeow TIDAK. Tapi quotedType SENDIRIAN
	//     tak cukup: WA Web ASLI juga me-reply TANPA quotedType (terbukti dari data) →
	//     kalau dipakai sendirian, WA Web asli salah divonis BOT.
	//
	//     PEMBEDA: format message-ID. Bot yang tertangkap memalsukan ID ANDROID
	//     ("AC"+hex, DeviceClass=android) — Az Clone (device 7) & Ashii (device 30)
	//     dua-duanya reply tanpa quotedType. HP Android ASLI (Azumi) membawanya. WA Web
	//     pakai ID web (3EB0, DeviceClass=web) → TIDAK kena aturan ini → tetap lolos.
	//
	//     Jadi kontradiksi inilah sinyalnya: ID ber-format Android (klien Android asli
	//     SELALU set quotedType saat reply) TAPI reply-nya tanpa quotedType = pemalsu.
	//     Gate: LIVE + COMPANION (device != 0; HP utama sudah lolos #4) + DeviceClass
	//     android + reply tanpa quotedType. WA Web/iPhone/desktop tak tersentuh.
	if liveEnvelope && r.IsSecondary && r.DeviceClass == "android" && r.Struct.ReplyNoQuotedType {
		r.Verdict = VerdictBot
		r.VerdictReason = "ID Android companion tapi reply tanpa quotedType (jejak Baileys)"
		r.DefinitiveBot = true
		return
	}

	// 6. BUKTI POSITIF MANUSIA — PERANGKAT TERTAUT (companion) RESMI. Sampai di sini
	//    sudah lolos semua sidik jari bot: bukan server bot, bukan BAE*, bukan HOSTED,
	//    bukan 3EB0-tanpa-metadata (di chat pribadi). Bila message-ID-nya cocok FORMAT
	//    client WhatsApp ASLI (android/ios/web/desktop — bukan "unknown"), ini perangkat
	//    tertaut milik MANUSIA: WhatsApp di HP lain via "perangkat tertaut", WA Web, atau
	//    WA Desktop. Semuanya companion (device-index != 0) PERSIS seperti bot library,
	//    jadi device-index SENDIRI tak pernah membuktikan bot. Inilah perbaikan utama:
	//    dulu companion tanpa DeviceListMetadata langsung divonis BOT, padahal pesan teks
	//    grup dari perangkat tertaut asli pun NORMAL tanpa DLM (Sender Key/skmsg).
	if liveEnvelope && !r.IsUnknownDevice && !r.IsBaileysID {
		// Primary (device 0) sudah ditangani #4; di sini pasti companion resmi.
		r.Verdict = VerdictHuman
		r.VerdictReason = "perangkat tertaut (companion) — klien WA asli, E2EE"
		return
	}

	// 7. Jalur quote (liveEnvelope=false): envelope sudah dibuang WA → data tak cukup,
	//    tetap UNKNOWN agar tak salah menuduh pesan lama siapa pun.
	if !liveEnvelope {
		r.Verdict = VerdictUnknown
		r.VerdictReason = "metadata tak lengkap (jalur quote) — tak bisa dipastikan"
		return
	}

	// 8. Tersisa: pesan LIVE dengan FORMAT message-ID TAK DIKENAL (tak cocok pola client
	//    WhatsApp mana pun) DAN tanpa bukti client resmi. Inilah satu-satunya kasus yang
	//    masih diklasifikasi BOT secara agresif — format ID asing kuat berkorelasi dengan
	//    library/bot custom. Captcha antibot tetap jadi penyaring akhir bila keliru.
	r.Verdict = VerdictBot
	r.VerdictReason = "format ID tak dikenal, tanpa metadata client resmi"
}

func computeBaileysScore(r *BotDetectionResult) int {
	s := 0

	// Sidik jari Baileys KUAT (BAE*) → sinyal TERKUAT.
	if r.StrongBaileys {
		s += 12
	} else if r.IsBaileysID {
		// ID 3EB0 ambigu yang tak didukung metadata device → sinyal sedang.
		s += 8
	}

	// ID unknown (tidak cocok client resmi)
	if r.IsUnknownDevice && !r.IsBaileysID {
		s += 5
	}

	// CATATAN: "MessageContextInfo tanpa DeviceListMetadata" TIDAK lagi diberi skor.
	// Pesan teks grup dari app WA resmi NORMAL tanpa DLM (dienkripsi Sender Key), jadi
	// absennya DLM bukan sinyal Baileys — dulu inilah penyebab HP asli divonis bot.

	// HOSTED encryption (non-E2EE standar) → client custom
	if r.IsHostedEncryption {
		s += 5
	}

	// Bot metadata terdeteksi
	if r.HasBotMetadata {
		s += 3
	}

	// Bot invoke wrapper
	if r.IsBotInvoke {
		s += 2
	}

	// CATATAN: addressing lid (IsSecondary) TIDAK lagi diberi skor — WhatsApp modern
	// memakai lid untuk SEMUA perangkat termasuk HP utama, jadi bukan sinyal bot.

	return s
}

// HasBotSignals: shortcut untuk cek apakah pesan mengandung sinyal bot.
func HasBotSignals(evt *events.Message) bool {
	r := DetectBot(evt)
	return r.Verdict == VerdictBot || r.Verdict == VerdictBaileys
}

// classifyBaileysID memeriksa sidik jari Baileys pada message-ID dan MEMISAHKAN
// antara bukti KUAT dan AMBIGU:
//
//	strong=true  → keluarga BAE* (BAE5/BAE0..). WA resmi TAK PERNAH memakainya →
//	               cukup untuk memvonis Baileys sendirian.
//	ambiguous=true → prefix 3EB0. Dipakai BERSAMA oleh WA Web/Desktop resmi dan
//	               Baileys lama → BUKAN bukti sendirian; harus dikuatkan absennya
//	               metadata multi-device (isUseDevice).
//
// prefix = potongan prefix yang cocok (untuk ditampilkan).
func classifyBaileysID(id string) (strong, ambiguous bool, prefix string) {
	if reBaileysStrong.MatchString(id) {
		return true, false, id[:4]
	}
	if reBaileysWeb.MatchString(id) {
		return false, true, "3EB0"
	}
	return false, false, ""
}

// CheckBaileysID — kompat lama: true bila ID membawa sidik jari Baileys apa pun.
func CheckBaileysID(id string) (bool, string) {
	strong, ambiguous, prefix := classifyBaileysID(id)
	return strong || ambiguous, prefix
}

// messageTypeName mengembalikan nama tipe pesan proto (mis. "extendedTextMessage",
// "conversation", "imageMessage") untuk ditampilkan di blok Target. Mengembalikan
// "unknown" bila pesan nil atau tipe tak dikenal.
func messageTypeName(m *waProto.Message) string {
	if m == nil {
		return "unknown"
	}
	switch {
	case m.Conversation != nil:
		return "conversation"
	case m.ExtendedTextMessage != nil:
		return "extendedTextMessage"
	case m.ImageMessage != nil:
		return "imageMessage"
	case m.VideoMessage != nil:
		return "videoMessage"
	case m.AudioMessage != nil:
		return "audioMessage"
	case m.StickerMessage != nil:
		return "stickerMessage"
	case m.DocumentMessage != nil:
		return "documentMessage"
	case m.ContactMessage != nil:
		return "contactMessage"
	case m.LocationMessage != nil:
		return "locationMessage"
	case m.ReactionMessage != nil:
		return "reactionMessage"
	case m.ProtocolMessage != nil:
		return "protocolMessage"
	case m.ButtonsResponseMessage != nil:
		return "buttonsResponseMessage"
	case m.ListResponseMessage != nil:
		return "listResponseMessage"
	case m.TemplateButtonReplyMessage != nil:
		return "templateButtonReplyMessage"
	default:
		return "unknown"
	}
}

// computeEngineChecks mengisi 4 sub-pemeriksaan gaya "ISBOT CHECKER". Tiap check
// "OK" bila lolos, atau penanda masalah singkat. liveEnvelope=false (jalur quote)
// berarti envelope raw sudah dibuang WA → TK check terbatas.
//
//	TK (Token)    — envelope/messageContextInfo (token e2ee) terbaca utuh
//	DV (Device)   — status & index device teridentifikasi (primary/secondary)
//	PF (Platform) — platform terdeteksi dari format ID
//	ID            — format/panjang message-ID dikenali (vs sidik jari Baileys)
func computeEngineChecks(r *BotDetectionResult, liveEnvelope bool) {
	// TK — token/envelope.
	switch {
	case !liveEnvelope:
		r.TKCheck = "LIMITED" // jalur quote: envelope raw sudah dibuang WA
	case r.IsHostedEncryption:
		r.TKCheck = "HOSTED" // token non-E2EE (business/cloud API)
	default:
		r.TKCheck = "OK"
	}

	// DV — device.
	if r.IsUseDevice || r.IsPrimary || r.IsSecondary {
		r.DVCheck = "OK"
	} else {
		r.DVCheck = "NODEV"
	}

	// PF — platform.
	if r.Platform != "" && r.Platform != "unknown" {
		r.PFCheck = "OK"
	} else {
		r.PFCheck = "UNKNOWN"
	}

	// ID — format message-ID.
	switch {
	case r.StrongBaileys:
		r.IDCheck = "BAILEYS"
	case r.IsBaileysID:
		r.IDCheck = "SUSPECT" // 3EB0 ambigu tanpa metadata device
	case r.IsUnknownDevice:
		r.IDCheck = "UNKNOWN"
	default:
		r.IDCheck = "OK"
	}
}
