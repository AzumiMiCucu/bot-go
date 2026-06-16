package src

import (
    "fmt"
    "strings"
    "sync"
)

// Variabel untuk tracking sesi game (diakses dari commands dan game_ws)
var (
    ActivePlayers  = make(map[string]bool)
    SessionMutex   sync.Mutex
)

// ClearPlayerSession menghapus player dari activePlayers saat game over
func ClearPlayerSession(playerAJID, playerBJID string) {
    SessionMutex.Lock()
    defer SessionMutex.Unlock()

    playerA := strings.Split(playerAJID, "@")[0]
    playerB := strings.Split(playerBJID, "@")[0]

    delete(ActivePlayers, playerA)
    delete(ActivePlayers, playerB)

    fmt.Printf("🗑️ Sesi player %s dan %s telah dihapus\n", playerA, playerB)
}
