// File: commands/aliases.go
package commands

// Hanya di file ini kita mengimpor folder src
import "bot-go/src"

// ==========================================
// 1. ALIAS UNTUK STRUCT & TYPE
// ==========================================
// Menggunakan tanda '=' untuk membuat Type Alias
type Command = src.Command
type ContextBot = src.ContextBot
type Middleware = src.Middleware
type HookType = src.HookType

// ==========================================
// 2. ALIAS UNTUK FUNGSI / VARIABEL GLOBAL
// ==========================================
// Memasukkan fungsi dari src ke dalam variabel di package commands
var RegisterCommand = src.RegisterCommand
var RateLimitMiddleware = src.RateLimitMiddleware
var OwnerOnlyMiddleware = src.OwnerOnlyMiddleware
var PremiumOnlyMiddleware = src.PremiumOnlyMiddleware
var GroupOnlyMiddleware = src.GroupOnlyMiddleware

// AndroidExtra → SendRequestExtra ber-ID custom android (untuk semua SendMessage).
var AndroidExtra = src.AndroidExtra

type ButtonBuilder = src.ButtonBuilder
type SelectionRow = src.SelectionRow
type SelectionSection = src.SelectionSection

var MatchCommand = src.MatchCommand
var HookBeforeExecute = src.HookBeforeExecute
var ExecuteWithMiddlewares = src.ExecuteWithMiddlewares
var ExecuteHooks = src.ExecuteHooks
var HookOnError = src.HookOnError
var HookAfterExecute = src.HookAfterExecute
