package servers

import (
    "sync"
    "time"
)

// ServerState represents the lifecycle status of a server entry.
type ServerState string

const (
    StateNew     ServerState = "new"     // discovered from master; never had a good poll
    StateOnline  ServerState = "online"  // at least one successful poll and currently reachable
    StateOffline ServerState = "offline" // had a good poll before, now failing
)

// ServerEntry holds metadata and dynamic status for a game server.
type ServerEntry struct {
    Address      string      `json:"address"`
    Hostname     string      `json:"hostname"`
    Map          string      `json:"map"`
    Mod          string      `json:"mod"`
    GameType     string      `json:"gametype"`
    Version      string      `json:"version"`
    PB           string      `json:"pb"`
    PlayerCount  int         `json:"player_count"`
    MaxPlayers   int         `json:"max_players"`
    Players      []string    `json:"players"`
    Polls        int         `json:"polls"`
    LastSeen     time.Time   `json:"last_seen"`
    Online       bool        `json:"online"`
    Protocol     int         `json:"protocol"`
    Bots         []string    `json:"bots"`
    BotCount     int         `json:"bot_count"`
    State        ServerState `json:"state"`
    FirstSeen    time.Time   `json:"first_seen"`
    LastAttempt  time.Time   `json:"last_attempt"`
    LastGoodPoll time.Time   `json:"last_good_poll"`
    MissedPolls  int         `json:"missed_polls"`
}

// in-memory store and configuration
var (
    serverList  = make(map[string]*ServerEntry)
    serverMutex sync.Mutex

    protocols  = []string{"57", "60", "84"}
    masterHost = "wolfmaster.idsoftware.com:27950"
)

