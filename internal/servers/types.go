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
    // Sources records which known master(s) have reported this server,
    // keyed by master label (see knownMasters), value is the last time that
    // master reported it. "Direct heartbeat" is a pseudo-source recorded
    // when the server heartbeats straight to our own UDP master instead of
    // (or in addition to) being found via discovery.
    Sources map[string]time.Time `json:"sources"`
}

// in-memory store and configuration
var (
    serverList  = make(map[string]*ServerEntry)
    serverMutex sync.Mutex

    // protocols are queried during discovery (getservers <proto>) against
    // every known master. "68" (Quake 3 Arena retail) and "71" (OpenArena)
    // were added 2026-08-13 after confirming real, sizeable populations on
    // dpmaster.deathmask.net (162 and 89 servers respectively) -- q3server_poller's
    // getstatus polling and the network/history/master-list machinery are
    // already protocol-agnostic (they group by whatever ServerEntry.Protocol
    // value comes back), so adding a protocol here is enough to pick up a
    // new game end-to-end. "82" was added the same day: polling a few of its
    // servers directly showed gamename "silEnT"/"jaymod", both popular ET
    // mods that bump the wire protocol away from stock 84 -- still Enemy
    // Territory, just a different client-incompatible build. Skipped:
    // protocol 67 (Q3A 1.31, only 2 servers) and 43 (unidentified, couldn't
    // confirm which game).
    protocols  = []string{"57", "60", "84", "82", "68", "71"}
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
// Three Quake 3 Arena-specific community masters were added 2026-08-13
// alongside Q3A/OpenArena discovery itself: master.ioquake3.org (the
// ioquake3 project's own master, by far the largest single source found --
// 840 servers on protocol 68 alone, more than 5x what dpmaster had),
// master.maverickservers.com (383), and master0.excessiveplus.net (193).
// Some overlap with dpmaster's own Q3A population is expected and handled
// the same way as everywhere else: serverList is keyed by address, so
// duplicates across masters just collapse.
var knownMasters = []MasterHostInfo{
    {Host: masterHost, Label: "Official (id Software)"},
    {Host: "master.iortcw.org:27950", Label: "master.iortcw.org (iortcw project)"},
    {Host: "etmaster.net:27950", Label: "etmaster.net (community)"},
    {Host: "dpmaster.deathmask.net:27950", Label: "dpmaster.deathmask.net (community)"},
    {Host: "master.ioquake3.org:27950", Label: "master.ioquake3.org (ioquake3 project)"},
    {Host: "master.maverickservers.com:27950", Label: "master.maverickservers.com (community)"},
    {Host: "master0.excessiveplus.net:27950", Label: "master0.excessiveplus.net (community)"},
}
