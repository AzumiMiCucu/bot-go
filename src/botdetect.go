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

// Baileys ID prefixes — library WhatsApp non-resmi (Baileys) punya
// format message-ID khas yang tak dipakai client resmi:
//
//	BAE5 — Baileys multi-device (paling umum)
//	3EB0 — Baileys varian / pre-MD
var (
	reBaileysID         = regexp.MustCompile(`^(BAE5|3EB0|BAE[0-9A-F]{2})`) // semua prefix BAE*
	reBaileysPrefixOnly = regexp.MustCompile(`^(BAE5|3EB0)`)                // explicit Baileys-only
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
	IsBaileysID     bool   // true bila message-ID cocok prefix Baileys
	BaileysIDPrefix string // prefix yang cocok (BAE5 / 3EB0 / dsb)
	BaileysScore    int    // skor keyakinan Baileys (0-25)

	// Device signals
	DeviceClass     string // ios / web / android / desktop / unknown (dari ClassifyDeviceFromID)
	IsUnknownDevice bool   // true bila DeviceClass == unknown
	AddressingMode  string // pn / lid — HANYA informasional (lid bukan sinyal bot, dipakai semua HP modern)
	IsMultiDevice   bool   // true bila ada DeviceListMetadata atau DeviceSentMeta

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
	}

	// ── 1. Classify message-ID (Baileys + device type) ──
	r.DeviceClass = ClassifyDeviceFromID(r.MessageID)
	r.IsUnknownDevice = r.DeviceClass == "unknown"

	r.IsBaileysID, r.BaileysIDPrefix = CheckBaileysID(r.MessageID)

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

	// ── 9. Kesimpulan akhir ──
	classifyVerdict(&r, true)

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
	r.IsUnknownDevice = r.DeviceClass == "unknown"
	r.IsBaileysID, r.BaileysIDPrefix = CheckBaileysID(stanzaID)

	// ── Addressing & server ──
	classifyAddressing(&r, participant)
	if pj, err := types.ParseJID(participant); err == nil {
		classifyDevice(&r, pj.Device)
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

	// IsBot = akun bot sungguhan (server-level). botMetadata/BotInvoke TIDAK dihitung
	// di sini — itu sinyal thread-bot (interaksi), bukan bukti pengirim adalah bot.
	r.IsBot = r.IsFromBotServer
	r.BaileysScore = computeBaileysScore(&r)

	// Jalur quote: metadata raw (DeviceListMetadata) biasanya SUDAH DIBUANG WA dari
	// salinan pesan yang di-reply, jadi kita tak bisa memakai sinyal "tanpa DLM".
	// Tandai sebagai metadata tak lengkap → verdict cenderung "unknown" bila tak ada
	// sinyal definitif (bot server / Baileys ID / HOSTED).
	classifyVerdict(&r, r.RawMessageContextInfo)
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
		return
	}

	// 2. ID khas Baileys (BAE5/3EB0) — sidik jari library tak resmi.
	if r.IsBaileysID {
		r.Verdict = VerdictBaileys
		r.VerdictReason = "ID Baileys (" + r.BaileysIDPrefix + ")"
		return
	}

	// 3. Enkripsi HOSTED (non-E2EE) — client business/cloud API, bukan app personal.
	if r.IsHostedEncryption {
		r.Verdict = VerdictBot
		r.VerdictReason = "enkripsi HOSTED (non-E2EE)"
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

	// 6. CATATAN: metadata thread-bot (botMetadata/botSecret/BotInvoke) sengaja TIDAK
	//    menghasilkan verdict apa pun — netral. Itu cuma menandai pesan ada di thread
	//    bot (manusia di WA Web pun membawanya saat membalas bot). Memberinya SUSPECT
	//    membuat manusia WA Web/Desktop kena captcha. Deteksi companion-bot diserahkan
	//    ke sinyal PERILAKU (flood/typing/spam command) di antibot.

	// 7. Tak ada bukti definitif. JANGAN menebak bot dari absennya bukti manusia.
	//    ID device tak dikenal → suspect (sinyal lemah, butuh konfirmasi perilaku);
	//    selain itu unknown. Bot yang menyamar (ID Android + metadata dibuang) sengaja
	//    dibiarkan unknown di sini — ditangkap oleh sinyal PERILAKU (flood/repeat/typing),
	//    bukan metadata satu pesan.
	if r.IsUnknownDevice {
		r.Verdict = VerdictSuspect
		r.VerdictReason = "ID device tak dikenal, metadata tak lengkap"
		return
	}
	r.Verdict = VerdictUnknown
	r.VerdictReason = "tak ada bukti bot definitif"
}

func computeBaileysScore(r *BotDetectionResult) int {
	s := 0

	// ID Baileys eksplisit → sinyal TERKUAT
	if r.IsBaileysID {
		s += 12
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

// CheckBaileysID memeriksa apakah message-ID cocok dengan pola Baileys.
// Mengembalikan (isBaileys, prefix). Dipakai oleh cekbot (partial analysis).
func CheckBaileysID(id string) (bool, string) {
	if m := reBaileysPrefixOnly.FindStringSubmatch(id); len(m) > 0 {
		return true, m[1]
	}
	if m := reBaileysID.FindStringSubmatch(id); len(m) > 0 {
		return true, m[1]
	}
	return false, ""
}
