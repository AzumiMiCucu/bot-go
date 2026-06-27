package src

import (
	"encoding/hex"
	"sort"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// =================================================================
// ENGINE SIDIK-JARI STRUKTURAL (level protobuf)
// =================================================================
// whatsmeow TIDAK membuka node XMPP mentah seperti Baileys (`ws.on('CB:message')`):
// pesan sudah di-decode jadi proto sebelum sampai ke handler. Tapi `evt.RawMessage`
// (proto MENTAH sebelum unwrap) + `UnknownFields` protobuf masih menyimpan jejak
// STRUKTURAL: field mana yang ter-set, pembungkus apa yang dipakai, dan tag proto
// asing yang dibawa klien. Dipakai untuk membedakan klien resmi vs Baileys/whatsmeow
// (mis. ReplyNoQuotedType & InteractiveCompose yang jadi sinyal vonis di botdetect.go).
// =================================================================

// StructReport = sidik jari struktural satu pesan.
type StructReport struct {
	Available   bool     // true bila berhasil dihitung dari proto mentah
	TopFields   []string // field Message tingkat-atas yang ter-set
	ContentType string   // tipe konten setelah unwrap (extendedTextMessage/conversation/dll)
	Wrappers    []string // pembungkus: deviceSentMessage/ephemeral/viewOnce/edited/documentWithCaption/botInvoke
	MCIFields   []string // subfield MessageContextInfo yang ter-set
	CtxFields   []string // subfield ContextInfo (di dalam konten) yang ter-set
	UnknownTop  string   // hex unknown-fields tingkat-atas (tag proto asing)
	UnknownMCI  string   // hex unknown-fields pada MessageContextInfo
	UnknownCtx  string   // hex unknown-fields pada ContextInfo
	Notes       []string // catatan struktural (informasional)
	Score       int      // skor anomali — SENGAJA 0 (informasional)

	// Sinyal "reply tanpa quotedType" — JEJAK BAILEYS yang TERBUKTI dari data lapangan
	// (Az Clone & Ashii Bot tak punya; HP asli Azumi punya). Klien WhatsApp asli SELALU
	// menempelkan field ContextInfo.quotedType (enum EXPLICIT/AUTO, field 71) saat
	// me-reply; Baileys/whatsmeow tidak. Hanya berlaku pada pesan REPLY.
	IsReply           bool // pesan ini me-reply pesan lain (ada quotedMessage/stanzaID)
	HasQuotedType     bool // ContextInfo membawa field quotedType (ciri klien asli)
	ReplyNoQuotedType bool // IsReply && !HasQuotedType → jejak Baileys

	// InteractiveCompose = pesan menyusun tombol/list/template/interactive. Tipe ini
	// TIDAK BISA dibuat user WhatsApp personal — hanya bot/business-API. Sinyal bot
	// yang berlaku TANPA perlu reply (mis. bot Theresav kirim interactiveMessage).
	// CATATAN: tipe RESPON (buttonsResponse/listResponse/interactiveResponse) TIDAK
	// dihitung — itu aksi USER menekan tombol, bukan bot.
	InteractiveCompose bool
}

// hasInteractiveCompose: true bila pesan menyusun tombol/list/template/interactive
// (tipe yang hanya bisa dibuat bot/business, bukan user personal).
func hasInteractiveCompose(m *waProto.Message) bool {
	if m == nil {
		return false
	}
	return m.GetInteractiveMessage() != nil || m.GetButtonsMessage() != nil ||
		m.GetListMessage() != nil || m.GetTemplateMessage() != nil
}

// containsStr: true bila slice memuat s.
func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// setFields mengembalikan nama field proto yang ter-set (terurut) + hex unknown-fields.
// Aman untuk pointer nil (mengembalikan kosong).
func setFields(m proto.Message) (names []string, unknownHex string) {
	if m == nil {
		return nil, ""
	}
	r := m.ProtoReflect()
	if !r.IsValid() { // pointer nil bertipe → message read-only kosong
		return nil, ""
	}
	r.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		names = append(names, string(fd.Name()))
		return true
	})
	sort.Strings(names)
	if u := r.GetUnknown(); len(u) > 0 {
		unknownHex = hex.EncodeToString(u)
	}
	return names, unknownHex
}

// contextInfoOf mengambil ContextInfo dari sebuah konten pesan (tipe yang umum).
func contextInfoOf(m *waProto.Message) *waProto.ContextInfo {
	if m == nil {
		return nil
	}
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	case m.GetContactMessage() != nil:
		return m.GetContactMessage().GetContextInfo()
	case m.GetLocationMessage() != nil:
		return m.GetLocationMessage().GetContextInfo()
	}
	return nil
}

// AnalyzeStructure menghitung sidik jari struktural dari proto MENTAH sebuah pesan
// (raw = evt.RawMessage, belum di-unwrap). Mengembalikan StructReport kosong (Available
// false) bila raw nil.
func AnalyzeStructure(raw *waProto.Message) StructReport {
	var rep StructReport
	if raw == nil {
		return rep
	}
	rep.Available = true

	// ── Field tingkat-atas + unknown-fields ──
	rep.TopFields, rep.UnknownTop = setFields(raw)

	// ── Pembungkus (wrapper) ──
	if raw.GetDeviceSentMessage().GetMessage() != nil {
		rep.Wrappers = append(rep.Wrappers, "deviceSentMessage")
	}
	if raw.GetEphemeralMessage().GetMessage() != nil {
		rep.Wrappers = append(rep.Wrappers, "ephemeralMessage")
	}
	if raw.GetViewOnceMessage().GetMessage() != nil || raw.GetViewOnceMessageV2().GetMessage() != nil ||
		raw.GetViewOnceMessageV2Extension().GetMessage() != nil {
		rep.Wrappers = append(rep.Wrappers, "viewOnceMessage")
	}
	if raw.GetDocumentWithCaptionMessage().GetMessage() != nil {
		rep.Wrappers = append(rep.Wrappers, "documentWithCaptionMessage")
	}
	if raw.GetEditedMessage().GetMessage() != nil {
		rep.Wrappers = append(rep.Wrappers, "editedMessage")
	}
	if raw.GetBotInvokeMessage().GetMessage() != nil {
		rep.Wrappers = append(rep.Wrappers, "botInvokeMessage")
	}

	// ── Konten (setelah unwrap) + ContextInfo-nya ──
	content := UnwrapMessage(raw)
	rep.ContentType = messageTypeName(content)
	if ci := contextInfoOf(content); ci != nil {
		rep.CtxFields, rep.UnknownCtx = setFields(ci)
	}

	// ── Sinyal "reply tanpa quotedType" (jejak Baileys, terbukti dari data) ──
	rep.IsReply = containsStr(rep.CtxFields, "stanzaID") || containsStr(rep.CtxFields, "quotedMessage")
	rep.HasQuotedType = containsStr(rep.CtxFields, "quotedType")
	rep.ReplyNoQuotedType = rep.IsReply && !rep.HasQuotedType

	// ── Tipe interaktif (tombol/list/template) — hanya bot/business bisa menyusunnya ──
	rep.InteractiveCompose = hasInteractiveCompose(raw) || hasInteractiveCompose(content)

	// ── MessageContextInfo (envelope token e2ee) ──
	if mci := raw.GetMessageContextInfo(); mci != nil {
		rep.MCIFields, rep.UnknownMCI = setFields(mci)
	}

	// ── Catatan struktural (INFORMASIONAL — bukan vonis) ──
	if rep.UnknownTop != "" {
		rep.Notes = append(rep.Notes, "ada tag proto asing di tingkat-atas")
	}
	if rep.UnknownMCI != "" {
		rep.Notes = append(rep.Notes, "ada tag proto asing di messageContextInfo")
	}
	if rep.UnknownCtx != "" {
		rep.Notes = append(rep.Notes, "ada tag proto asing di contextInfo")
	}
	if len(rep.MCIFields) == 1 && rep.MCIFields[0] == "messageSecret" {
		rep.Notes = append(rep.Notes, "messageContextInfo minimal (hanya messageSecret)")
	}

	return rep
}
