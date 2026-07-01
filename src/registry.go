package src

import (
	"context"
	//"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// ==========================================
// 1. STRUKTUR DATA UTAMA (TYPES)
// ==========================================

type Middleware func(*ContextBot) error

type Command struct {
	Name         string
	Category     string
	Aliases      []string
	Pattern      *regexp.Regexp
	Description  string
	Execute      func(*ContextBot) error
	Price        float64
	Cooldown     time.Duration
	// Premium: bila true, command HANYA bisa dijalankan user premium (owner selalu
	// premium), dan HASIL-nya di grup disembunyikan dari member non-premium lewat
	// sistem exclude (mereka dapat placeholder "Menunggu pesan ini"). Set di
	// RegisterCommand → skalabel: tambah fitur premium cukup `Premium: true`.
	Premium      bool
	Middlewares  []Middleware
	LastExecuted map[string]time.Time
	mu           sync.RWMutex
}

type ContextBot struct {
	Client        *whatsmeow.Client
	Msg           *events.Message
	ChatJID       types.JID
	SenderJID     types.JID
	SenderAlt     types.JID
	User          string
	Args          string
	TextMessage   string
	PushName      string
	UserBalance   float64
	IsOwner       bool
	IsPremium     bool
	IsGroup       bool
	AddBalance    func(float64) float64
	DeductBalance func(float64) (bool, float64)
	Reply         func(string) error
	ReplyWithID   func(string) (string, error)
	React         func(string) error
	Print         func(data ...interface{})
	Ctx           context.Context
	Button        func() *ButtonBuilder
	AIRich func() * AIRichBuilder
}

type HookType string

const (
	HookBeforeExecute HookType = "before_execute"
	HookAfterExecute  HookType = "after_execute"
	HookOnError       HookType = "on_error"
)

type Hook struct {
	Type     HookType
	Callback func(*ContextBot, *Command, error) error
}

// ==========================================
// 2. VARIABEL GLOBAL & INIT
// ==========================================

var (
	// CommandRegistry menyimpan POINTER ke Command (bukan nilai) supaya:
	//   • mutex & map cooldown di dalam Command tak pernah dikopi (race-safe);
	//   • pointer yang dikembalikan RegisterCommand tetap valid walau slice realokasi.
	CommandRegistry []*Command
	registryMutex   sync.RWMutex

	// Index command untuk pencocokan cepat (dibangun ulang tiap RegisterCommand)
	exactAliasMap   = make(map[string]*Command) // alias lower → command (exact match O(1))
	patternCommands []*Command                  // hanya command yang punya Pattern (regex)
	prefixEntries   []prefixEntry               // alias non-pattern (untuk prefix match, urut registrasi)

	hooks      = make(map[HookType][]Hook)
	hooksMutex sync.RWMutex
)

type prefixEntry struct {
	alias string
	cmd   *Command
}

// rebuildIndex membangun ulang index pencocokan dari CommandRegistry.
// Wajib dipanggil saat memegang registryMutex (write lock).
func rebuildIndex() {
	exactAliasMap = make(map[string]*Command, len(CommandRegistry)*2)
	patternCommands = patternCommands[:0]
	prefixEntries = prefixEntries[:0]

	for i := range CommandRegistry {
		cmd := CommandRegistry[i]
		if cmd.Pattern != nil {
			patternCommands = append(patternCommands, cmd)
		}
		for _, alias := range cmd.Aliases {
			al := strings.ToLower(alias)
			// first-registered menang (jaga perilaku lama)
			if _, ok := exactAliasMap[al]; !ok {
				exactAliasMap[al] = cmd
			}
			if cmd.Pattern == nil {
				prefixEntries = append(prefixEntries, prefixEntry{alias: al, cmd: cmd})
			}
		}
	}
}

// ==========================================
// 3. REGISTRY & MATCHER (SEDERHANA & CEPAT)
// ==========================================

func RegisterCommand(cmd Command) *Command {
	registryMutex.Lock()
	defer registryMutex.Unlock()

	cmd.LastExecuted = make(map[string]time.Time)
	c := &cmd
	CommandRegistry = append(CommandRegistry, c)
	rebuildIndex()
	return c
}

// MatchCommand adalah otak pendeteksi (Classic Prefix-less)
func MatchCommand(text string) (*Command, string) {
	registryMutex.RLock()
	defer registryMutex.RUnlock()

	text = strings.TrimSpace(text)
	if text == "" {
		return nil, ""
	}
	// Early-exit: pesan sangat panjang hampir pasti bukan command → lewati loop regex
	if len(text) > 2000 {
		return nil, ""
	}

	// 1. Cek Regex Matching (Prioritas Tertinggi) — hanya command ber-Pattern
	for _, cmd := range patternCommands {
		if cmd.Pattern.MatchString(text) {
			matches := cmd.Pattern.FindStringSubmatch(text)
			args := ""
			if len(matches) > 1 {
				args = strings.TrimSpace(matches[1])
			} else {
				args = strings.TrimSpace(cmd.Pattern.ReplaceAllString(text, ""))
			}
			return cmd, args
		}
	}

	// 2. Exact Match (O(1) via index)
	textLower := strings.ToLower(text)
	if cmd, ok := exactAliasMap[textLower]; ok {
		return cmd, ""
	}

	// 3. Prefix Match (Alias + Spasi + Argumen) — hanya command non-Pattern
	for _, pe := range prefixEntries {
		if strings.HasPrefix(textLower, pe.alias+" ") {
			args := strings.TrimSpace(text[len(pe.alias)+1:])
			return pe.cmd, args
		}
	}

	return nil, ""
}

func (cmd *Command) Use(middlewares ...Middleware) *Command {
	if cmd != nil {
		cmd.Middlewares = append(cmd.Middlewares, middlewares...)
	}
	return cmd
}

func (cmd *Command) IsCooledDown(userID string) bool {
	if cmd.Cooldown == 0 {
		return true
	}
	cmd.mu.RLock()
	lastTime, exists := cmd.LastExecuted[userID]
	cmd.mu.RUnlock()

	if !exists {
		return true
	}
	return time.Since(lastTime) >= cmd.Cooldown
}

func (cmd *Command) SetCooldown(userID string) {
	cmd.mu.Lock()
	defer cmd.mu.Unlock()
	cmd.LastExecuted[userID] = time.Now()
}

// ==========================================
// 4. MIDDLEWARES
// ==========================================

func RateLimitMiddleware(maxRequests int, duration time.Duration) Middleware {
	requestCounts := make(map[string][]time.Time)
	mu := sync.RWMutex{}

	return func(ctx *ContextBot) error {
		userID := ctx.User
		now := time.Now()

		mu.Lock()
		defer mu.Unlock()

		var validRequests []time.Time
		for _, t := range requestCounts[userID] {
			if now.Sub(t) < duration {
				validRequests = append(validRequests, t)
			}
		}

		if len(validRequests) >= maxRequests {
			return fmt.Errorf("⏱️ Terlalu banyak request. Coba lagi dalam %v", duration)
		}

		requestCounts[userID] = append(validRequests, now)
		return nil
	}
}

func OwnerOnlyMiddleware(ctx *ContextBot) error {
	if !ctx.IsOwner {
		return fmt.Errorf(" ")
	}
	return nil
}

func PremiumOnlyMiddleware(ctx *ContextBot) error {
	if ctx.UserBalance < 1.0 {
		return fmt.Errorf("💳 Saldo tidak cukup untuk fitur premium")
	}
	return nil
}

func ExecuteWithMiddlewares(ctx *ContextBot, cmd *Command) error {
	for _, mw := range cmd.Middlewares {
		if err := mw(ctx); err != nil {
			return err
		}
	}
	return cmd.Execute(ctx)
}

func GroupOnlyMiddleware(ctx *ContextBot) error {
	if !ctx.IsGroup {
		return fmt.Errorf(" ")
	}
	return nil
}

// ==========================================
// 5. HOOKS (EVENT LISTENERS)
// ==========================================

func RegisterHook(hookType HookType, callback func(*ContextBot, *Command, error) error) {
	hooksMutex.Lock()
	defer hooksMutex.Unlock()

	hooks[hookType] = append(hooks[hookType], Hook{
		Type:     hookType,
		Callback: callback,
	})
}

func ExecuteHooks(hookType HookType, ctx *ContextBot, cmd *Command, err error) error {
	hooksMutex.RLock()
	defer hooksMutex.RUnlock()

	for _, hook := range hooks[hookType] {
		if err := hook.Callback(ctx, cmd, err); err != nil {
			fmt.Printf("[HOOK] Hook %s gagal: %v\n", hookType, err)
			return err
		}
	}
	return nil
}

// Tambahkan ini di paling bawah file src/registry.go
func GetCommands() []*Command {
	registryMutex.RLock()
	defer registryMutex.RUnlock()
	return CommandRegistry
}
