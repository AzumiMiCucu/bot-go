package src

import (
	waProto "go.mau.fi/whatsmeow/binary/proto"
)

// =================================================================
// KLASIFIKASI JENIS PESAN — dipakai statistik grup (gstats).
// Mengembalikan SATU kategori jenis pesan dari sebuah protobuf WhatsApp.
// Membuka bungkus view-once/documentWithCaption lebih dulu (UnwrapMessage).
// =================================================================

// Konstanta jenis pesan. Nilai string ini juga yang disimpan ke DB
// (group_msg_kinds.kind), jadi JANGAN diubah sembarangan agar histori konsisten.
const (
	KindText     = "text"
	KindImage    = "image"
	KindVideo    = "video"
	KindGif      = "gif"
	KindSticker  = "sticker"
	KindVoice    = "voice" // audio PTT (rekaman suara)
	KindAudio    = "audio" // audio non-PTT (musik/berkas suara)
	KindDocument = "document"
	KindPoll     = "poll"
	KindLocation = "location"
	KindContact  = "contact"
	KindReaction = "reaction"
	KindOther    = "other"
)

// MessageKind mengklasifikasikan jenis sebuah pesan. Urutan pengecekan dipilih
// agar tipe media spesifik menang lebih dulu sebelum fallback teks/other.
func MessageKind(m *waProto.Message) string {
	m = UnwrapMessage(m)
	if m == nil {
		return KindOther
	}

	switch {
	case m.GetImageMessage() != nil:
		return KindImage
	case m.GetVideoMessage() != nil:
		if m.GetVideoMessage().GetGifPlayback() {
			return KindGif
		}
		return KindVideo
	case m.GetStickerMessage() != nil:
		return KindSticker
	case m.GetAudioMessage() != nil:
		if m.GetAudioMessage().GetPTT() {
			return KindVoice
		}
		return KindAudio
	case m.GetDocumentMessage() != nil:
		return KindDocument
	case m.GetPollCreationMessage() != nil,
		m.GetPollCreationMessageV2() != nil,
		m.GetPollCreationMessageV3() != nil:
		return KindPoll
	case m.GetLocationMessage() != nil, m.GetLiveLocationMessage() != nil:
		return KindLocation
	case m.GetContactMessage() != nil, m.GetContactsArrayMessage() != nil:
		return KindContact
	case m.GetReactionMessage() != nil:
		return KindReaction
	case m.GetConversation() != "", m.GetExtendedTextMessage() != nil:
		return KindText
	}
	return KindOther
}

// KindLabel mengembalikan label tampilan (Indonesia + emoji) untuk sebuah jenis.
func KindLabel(kind string) string {
	switch kind {
	case KindText:
		return "💬 Teks"
	case KindImage:
		return "🖼️ Gambar"
	case KindVideo:
		return "🎬 Video"
	case KindGif:
		return "📽️ GIF"
	case KindSticker:
		return "🎭 Stiker"
	case KindVoice:
		return "🎙️ Suara"
	case KindAudio:
		return "🎵 Audio"
	case KindDocument:
		return "📄 Dokumen"
	case KindPoll:
		return "📊 Polling"
	case KindLocation:
		return "📍 Lokasi"
	case KindContact:
		return "👤 Kontak"
	case KindReaction:
		return "❤️ Reaksi"
	default:
		return "❔ Lainnya"
	}
}
