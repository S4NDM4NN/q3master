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
    // Heartbeat observability
    LastHeartbeat time.Time `json:"last_heartbeat"`
    Heartbeats    int       `json:"heartbeat_count"`
}

// in-memory store and configuration
var (
    serverList  = make(map[string]*ServerEntry)
    serverMutex sync.Mutex

    protocols  = []string{"57", "60", "84"}
    masterHost = "wolfmaster.idsoftware.com:27950"
)

// MasterHostInfo names a master server we query for discovery and track
// status/history for.
type MasterHostInfo struct {
    Host  string `json:"host"`
    Label string `json:"label"`
}

// knownMasters is queried for server addresses to poll (discovery) and its
// per-host reachability is tracked and recorded to history (the "master
// servers" status page). It includes masterHost (the official id Software
// master -- also tracked individually via MasterStatus/history for the
// main page's status line, since it's been the one worth watching most
// closely, and has been unreliable) plus three others that have stepped in
// while it's down: master.iortcw.org (only sv_master2's default for
// servers actually running the iortcw engine -- most RTCW/ET servers
// aren't, so it's a master to add explicitly, not something already
// covering them), etmaster.net (an explicit replacement for the official
// ET master), and dpmaster.deathmask.net (a long-running generic idTech3
// master, also used by many RTCW servers). Confirmed 2026-08-13: wolfmaster
// returned zero servers across all three protocols while etmaster.net
// alone returned ~149 ET servers, dpmaster ~57 RTCW 1.4 servers, and
// master.iortcw.org ~13 RTCW 1.4 + 1 RTCW 1.0 -- querying only the official
// master was silently missing most of the active server population.
var knownMasters = []MasterHostInfo{
    {Host: masterHost, Label: "Official (id Software)"},
    {Host: "master.iortcw.org:27950", Label: "master.iortcw.org (iortcw project)"},
    {Host: "etmaster.net:27950", Label: "etmaster.net (community)"},
    {Host: "dpmaster.deathmask.net:27950", Label: "dpmaster.deathmask.net (community)"},
}
