package commands

import (
	"sync"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// =================================================================
// RAW MESSAGE CACHE — pesan MENTAH & LENGKAP per StanzaID.
// =================================================================
//
// Masalah: ContextInfo.QuotedMessage yang menempel pada pesan reply adalah
// versi yang sudah DI-DEGRADE oleh WhatsApp — banyak field (ContextInfo
// bersarang, metadata media, bahkan tipe konten) dibuang. Akibatnya `crm`
// yang menserialisasi quoted itu tak pernah menangkap rawMessage sungguhan;
// pesan seperti list/tombol/media malah tampil sebagai `conversation` biasa.
//
// Solusi: tiap pesan masuk DICACHE utuh (evt.Message) dengan kunci StanzaID.
// Saat `crm`/`run`/`resend` mereferensikan pesan yang di-reply, kita ambil
// versi PENUH dari cache ini (fallback ke quoted yang di-degrade bila miss,
// mis. pesan lama sebelum bot menyala). Bounded + TTL agar hemat memori.

const (
	rawCacheTTL     = 30 * time.Minute
	rawCacheMaxSize = 4000 // batas keras entri; over → buang yang terlama
)

type rawCacheEntry struct {
	msg       *waProto.Message
	expiresAt time.Time
	storedAt  time.Time
}

type rawMessageCache struct {
	mu    sync.RWMutex
	store map[string]*rawCacheEntry
}

var rawMsgCache = newRawMessageCache()

func newRawMessageCache() *rawMessageCache {
	c := &rawMessageCache{store: make(map[string]*rawCacheEntry)}
	go c.cleanup()
	return c
}

// CacheRawMessage menyimpan pesan mentah lengkap berkunci StanzaID.
// Dipanggil dari handler untuk SETIAP pesan masuk (termasuk pesan bot sendiri).
func CacheRawMessage(stanzaID string, msg *waProto.Message) {
	if stanzaID == "" || msg == nil {
		return
	}
	c := rawMsgCache
	c.mu.Lock()
	defer c.mu.Unlock()

	// Cap ukuran: bila penuh, buang entri terlama (jarang terjadi, cukup linear).
	if len(c.store) >= rawCacheMaxSize {
		var oldestID string
		var oldest time.Time
		for id, e := range c.store {
			if oldestID == "" || e.storedAt.Before(oldest) {
				oldestID, oldest = id, e.storedAt
			}
		}
		if oldestID != "" {
			delete(c.store, oldestID)
		}
	}

	now := time.Now()
	c.store[stanzaID] = &rawCacheEntry{
		msg:       proto.Clone(msg).(*waProto.Message),
		expiresAt: now.Add(rawCacheTTL),
		storedAt:  now,
	}
}

// GetRawMessage mengambil pesan mentah penuh berdasarkan StanzaID (bila masih ada).
func GetRawMessage(stanzaID string) (*waProto.Message, bool) {
	if stanzaID == "" {
		return nil, false
	}
	c := rawMsgCache
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.store[stanzaID]
	if ok && time.Now().Before(e.expiresAt) {
		return e.msg, true
	}
	return nil, false
}

// resolveQuotedFull mengembalikan pesan yang di-reply dalam bentuk PALING lengkap:
// utamakan versi mentah dari cache (via StanzaID), fallback ke quoted yang
// di-degrade dari ContextInfo. Mengembalikan (msg, true) bila ada apa pun.
func resolveQuotedFull(ci *waProto.ContextInfo) (*waProto.Message, bool) {
	if ci == nil {
		return nil, false
	}
	if full, ok := GetRawMessage(ci.GetStanzaID()); ok {
		return full, true
	}
	if q := ci.GetQuotedMessage(); q != nil {
		return q, true
	}
	return nil, false
}

func (c *rawMessageCache) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for id, e := range c.store {
			if now.After(e.expiresAt) {
				delete(c.store, id)
			}
		}
		c.mu.Unlock()
	}
}
