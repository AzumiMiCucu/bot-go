package commands

import (
	"sync"
	"time"
)

// =================================================================
// REPLY ROUTER — Interaksi berbasis ID pesan (bukan sesi per-user)
// =================================================================
//
// Konsep:
//   - Saat bot mengirim pesan list/menu, ID pesan terkirim ditangkap dan
//     didaftarkan ke router ini bersama state-nya (Register).
//   - Saat ada pesan masuk yang me-reply, kita ambil StanzaID pesan yang
//     di-quote (quotedMsgID). Jika cocok dengan entri router, handler tipe
//     terkait dijalankan — TANPA peduli siapa pengirimnya (siapa saja boleh
//     membalas). ID pesan ini tak terlihat di teks namun melekat di metadata.
//   - Reply ke pesan non-bot, atau pesan tanpa quote, diabaikan.

type ReplyContext struct {
	Type      string      // "donghua","shinigami","kompas","samehada","soundcloud"
	Data      interface{} // pointer ke struct state per-fitur
	ExpiresAt time.Time
}

type ReplyRouter struct {
	mu    sync.RWMutex
	store map[string]*ReplyContext // key = ID pesan bot yang akan di-reply
}

const replyTTL = 30 * time.Minute

var replyRouter = newReplyRouter()

func newReplyRouter() *ReplyRouter {
	r := &ReplyRouter{store: make(map[string]*ReplyContext)}
	go r.cleanup()
	return r
}

// Register mengaitkan ID pesan bot dengan state interaksi.
func (r *ReplyRouter) Register(msgID, typ string, data interface{}) {
	if msgID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[msgID] = &ReplyContext{
		Type:      typ,
		Data:      data,
		ExpiresAt: time.Now().Add(replyTTL),
	}
}

// Get mengambil state berdasarkan ID pesan yang di-reply (hormati expiry).
func (r *ReplyRouter) Get(msgID string) (*ReplyContext, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rc, ok := r.store[msgID]
	if ok && time.Now().Before(rc.ExpiresAt) {
		return rc, true
	}
	return nil, false
}

func (r *ReplyRouter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		r.mu.Lock()
		now := time.Now()
		for id, rc := range r.store {
			if now.After(rc.ExpiresAt) {
				delete(r.store, id)
			}
		}
		r.mu.Unlock()
	}
}

// quotedMsgID mengambil ID pesan yang di-reply (StanzaID) dari pesan masuk.
// Kosong bila pesan ini bukan sebuah reply.
func quotedMsgID(ctx *ContextBot) string {
	if ctx == nil || ctx.Msg == nil {
		return ""
	}
	ci := ctx.Msg.Message.GetExtendedTextMessage().GetContextInfo()
	if ci == nil {
		return ""
	}
	return ci.GetStanzaID()
}

// RouteReply menangani pesan yang me-reply sebuah pesan bot terdaftar.
// Mengembalikan true bila pesan dikonsumsi (cocok dengan entri router).
func RouteReply(ctx *ContextBot) bool {
	qid := quotedMsgID(ctx)
	if qid == "" {
		return false
	}
	rc, ok := replyRouter.Get(qid)
	if !ok {
		return false
	}

	switch rc.Type {
	case "donghua":
		_ = handleDonghuaReply(ctx, rc)
	case "shinigami":
		_ = handleShinigamiReply(ctx, rc)
	case "kompas":
		_ = handleKompasReply(ctx, rc)
	case "samehada":
		_ = handleSamehadaReply(ctx, rc)
	case "soundcloud":
		_ = handleSoundcloudReply(ctx, rc)
	case "referral":
		_ = handleReferralClaim(ctx, rc)
	case "ytmusic":
		// Hanya konsumsi bila reply benar-benar memilih lagu dari daftar.
		return handleYtMusicReply(ctx, rc)
	default:
		return false
	}
	return true
}
