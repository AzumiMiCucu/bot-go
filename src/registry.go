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
	IsGroup bool
	Memory        *ConversationMemory
	AddBalance    func(float64) float64
	DeductBalance func(float64) (bool, float64)
	Reply         func(string) error
	ReplyWithID   func(string) (string, error)
	React func(string) error
	Print          func(data ...interface{})
	Ctx           context.Context
	Button        func() *ButtonBuilder
}

type ConversationMemory struct {
	UserID       string
	Messages     []MemoryMessage
	Context      map[string]interface{}
	LastActivity time.Time
	mu           sync.RWMutex
}

type MemoryMessage struct {
	Timestamp time.Time              `json:"timestamp"`
	Sender    string                 `json:"sender"`
	Content   string                 `json:"content"`
	Metadata  map[string]interface{} `json:"metadata"`
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
	CommandRegistry    []Command
	registryMutex      sync.RWMutex
	
	conversationMemory = make(map[string]*ConversationMemory)
	memoryMutex        sync.RWMutex
	
	hooks              = make(map[HookType][]Hook)
	hooksMutex         sync.RWMutex
)

func init() {
	// Auto cleanup memori chat tiap 15 menit
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		for range ticker.C {
			memoryMutex.Lock()
			now := time.Now()
			for k, v := range conversationMemory {
				if now.Sub(v.LastActivity) > 30*time.Minute {
					delete(conversationMemory, k)
				}
			}
			memoryMutex.Unlock()
		}
	}()
}

// ==========================================
// 3. REGISTRY & MATCHER (SEDERHANA & CEPAT)
// ==========================================

func RegisterCommand(cmd Command) *Command {
	registryMutex.Lock()
	defer registryMutex.Unlock()

	cmd.LastExecuted = make(map[string]time.Time)
	CommandRegistry = append(CommandRegistry, cmd)
	return &CommandRegistry[len(CommandRegistry)-1]
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

	// 1. Cek Regex Matching (Prioritas Tertinggi untuk pola khusus)
	for i := range CommandRegistry {
		cmd := &CommandRegistry[i]
		if cmd.Pattern != nil && cmd.Pattern.MatchString(text) {
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

// 2. Exact Match & Prefix-less (Pencocokan Langsung, TANPA NLP)
	textLower := strings.ToLower(text)

	for i := range CommandRegistry {
		cmd := &CommandRegistry[i]
		for _, alias := range cmd.Aliases {
			aliasLower := strings.ToLower(alias)

			// ATURAN 1: TEPAT SAMA persis (Exact Match)
			// Ini selalu bekerja. Jadi ketik "list" saja akan selalu memanggil fitur.
			if textLower == aliasLower {
				return cmd, ""
			}

			// ==========================================
			// 🚨 REGEX MUTLAK:
			// Jika command ini punya Pattern (Regex), 
			// JANGAN lanjut ke pencocokan Prefix (spasi tambahan).
			// Biarkan Regex (di Step 1 atas) yang mengurus argumennya!
			if cmd.Pattern != nil {
				continue 
			}
			// ==========================================

			// ATURAN 2: PREFIX MATCH (Alias + Spasi + Argumen)
			// Hanya berlaku untuk command yang TIDAK pakai Regex!
			if strings.HasPrefix(textLower, aliasLower+" ") {
				// Sisa kalimat setelah alias dianggap sebagai argumen
				args := strings.TrimSpace(text[len(aliasLower)+1:])
				return cmd, args
			}
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
// 5. MEMORY CHAT / CONTEXT
// ==========================================

func GetMemory(userID string) *ConversationMemory {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()

	if mem, exists := conversationMemory[userID]; exists {
		mem.LastActivity = time.Now()
		return mem
	}

	mem := &ConversationMemory{
		UserID:       userID,
		Messages:     make([]MemoryMessage, 0),
		Context:      make(map[string]interface{}),
		LastActivity: time.Now(),
	}

	conversationMemory[userID] = mem
	return mem
}

func (cm *ConversationMemory) AddMessage(sender, content string, metadata map[string]interface{}) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.LastActivity = time.Now()
	cm.Messages = append(cm.Messages, MemoryMessage{
		Timestamp: time.Now(),
		Sender:    sender,
		Content:   content,
		Metadata:  metadata,
	})

	if len(cm.Messages) > 15 {
		cm.Messages = cm.Messages[len(cm.Messages)-15:]
	}
}

func (cm *ConversationMemory) GetChatHistory() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	history := ""
	for _, msg := range cm.Messages {
		history += msg.Sender + ": " + msg.Content + "\n"
	}
	return history
}

// ==========================================
// 6. HOOKS (EVENT LISTENERS)
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
func GetCommands() []Command {
	registryMutex.RLock()
	defer registryMutex.RUnlock()
	return CommandRegistry
}