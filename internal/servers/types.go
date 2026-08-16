package servers

import (
    "sort"
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
    // AlsoKnownAs lists other addresses detected (see clones.go) reporting
    // identical map/player content to this one -- almost certainly the
    // same physical server broadcasting under multiple ports/IPs, possibly
    // under different hostnames too (e.g. region-labeled mirrors of one
    // live match). Those addresses are collapsed out of the main list (see
    // detectClones) and folded into this field on the one entry kept, so
    // the server is shown once with an honest "also known as" note instead
    // of inflating the list N times.
    AlsoKnownAs []AKAEntry `json:"also_known_as,omitempty"`
    // MatchTimeSec/HasMatchTime capture the Q3A-family "Score_Time" field
    // (elapsed seconds in the current match) as of LastGoodPoll. Only
    // baseq3-derived mods (Q3A, OpenArena) send this -- ET/RTCW don't --
    // HasMatchTime distinguishes "server doesn't report this" from a
    // genuine 0:00. Used by clones.go to corroborate clone detection for
    // servers with no players to fingerprint by roster.
    MatchTimeSec int  `json:"match_time_sec,omitempty"`
    HasMatchTime bool `json:"has_match_time,omitempty"`
    // SuspectedBots lists names from Players (not Bots -- those already
    // self-report honestly) that have been continuously connected past
    // suspectedBotSessionThreshold (see playertrack.go) without ever
    // dropping out of the roster -- a pattern consistent with a spoofed
    // fake-player entry rather than a real, eventually-disconnecting
    // human. A heuristic, not a certainty: doesn't catch every spoofed
    // bot, and (rarely) could flag a genuinely very-long AFK session.
    SuspectedBots []string `json:"suspected_bots,omitempty"`
}

// in-memory store and configuration
var (
    serverList  = make(map[string]*ServerEntry)
    serverMutex sync.Mutex

    // protocols are queried during discovery (getservers <proto>) against
    // every known master. "68" (Quake 3 Arena retail) and "71" (OpenArena)
    // were added 2026-08-13 after confirming real, sizeable populations on
    // dpmaster.deathmask.net -- q3server_poller's
    // already protocol-agnostic (they group by whatever ServerEntry.Protocol
    // value comes back), so adding a protocol here is enough to pick up a
    // new game end-to-end. "82" was added the same day: polling a few of its
    // servers directly showed gamename "silEnT"/"jaymod", both popular ET
    // mods that bump the wire protocol away from stock 84 -- still Enemy
    // Territory, just a different client-incompatible build. "61" was added
    // after s4ndmod26's own iortcw-based servers (running iortcw builds
    // like "1.51c-MP" and a custom "S4NDMoD" one) showed up as unclassified
    // "Unknown" -- confirmed via ~/s4ndmod26/iortcw/code/qcommon/qcommon.h's
    // PROTOCOL_VERSION #define: iortcw bumped RTCW's protocol to 61 (one
    // above stock 1.4's 60), still RTCW under the hood. Skipped: protocol 67
    // (Q3A 1.31, negligible population) and 43 (unidentified, couldn't
    // confirm which game).
    protocols  = []string{"57", "60", "61", "84", "82", "68", "71"}
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
// returned zero servers across all three protocols while etmaster.net,
// dpmaster, and master.iortcw.org each returned meaningful populations --
// querying only the official master was silently missing most of the
// active server population. Three Quake 3 Arena-specific community masters
// were added 2026-08-13 alongside Q3A/OpenArena discovery itself:
// master.ioquake3.org (the ioquake3 project's own master, by far the
// largest single source found), master.maverickservers.com, and
// master0.excessiveplus.net. Some overlap with dpmaster's own Q3A
// population is expected and handled
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

// IgnoredHost is a manually curated block on an entire IP (every port on
// it), for hosts caught manipulating the list rather than just being a
// normal, honestly-reported server.
type IgnoredHost struct {
    IP     string `json:"ip"`
    Reason string `json:"reason"`
}

// ignoredHosts is checked by IP (port-independent) at every point a server
// can enter serverList -- discovery (queryMaster) and direct heartbeats
// (handleHeartbeat) -- so a blocked host's entries never appear on the list
// or count toward totals, regardless of which port or discovery path they
// come in on. This is a manual, curated list reserved for hosts doing
// something actually malicious (not just inflating their listing count --
// see clones.go for that, which collapses rather than bans). Empty by
// default; add entries here only for confirmed bad actors worse than
// ordinary list-padding.
//
// 155.138.197.166 (RETRO|FFA, confirmed 2026-08-13 padding itself across 8
// ports) was removed from here 2026-08-13 in favor of the automatic
// clone-collapsing in clones.go, which now handles this class of case
// without a manual entry or public ban.
var ignoredHosts = []IgnoredHost{}

var (
    ignoredSightings      = make(map[string]time.Time) // address -> last time it tried to register while blocked
    ignoredSightingsMutex sync.Mutex
)

// ignoredReasonForIP returns the block reason if ip (no port) matches a
// curated blocked host, and whether it matched at all.
func ignoredReasonForIP(ip string) (string, bool) {
    for _, h := range ignoredHosts {
        if h.IP == ip {
            return h.Reason, true
        }
    }
    return "", false
}

// checkIgnored splits addr ("ip:port") and reports whether its IP is
// blocked, recording a sighting (for the ignored-hosts page) if so.
func checkIgnored(addr string) bool {
    ip := ipOf(addr)
    if _, blocked := ignoredReasonForIP(ip); blocked {
        ignoredSightingsMutex.Lock()
        ignoredSightings[addr] = time.Now()
        ignoredSightingsMutex.Unlock()
        return true
    }
    return false
}

// IgnoredHostView is one blocked IP plus the specific addresses observed
// trying to register under it, for the "ignored" page.
type IgnoredHostView struct {
    IP        string            `json:"ip"`
    Reason    string            `json:"reason"`
    Addresses []IgnoredSighting `json:"addresses"`
}

// IgnoredSighting is one observed address (a specific port) on a blocked
// IP, and when it was last seen trying to register.
type IgnoredSighting struct {
    Address  string    `json:"address"`
    LastSeen time.Time `json:"last_seen"`
}

// GetIgnoredHosts returns every curated blocked IP, with the specific
// addresses seen trying to register under each, most-recently-seen first.
func GetIgnoredHosts() []IgnoredHostView {
    ignoredSightingsMutex.Lock()
    defer ignoredSightingsMutex.Unlock()

    out := make([]IgnoredHostView, 0, len(ignoredHosts))
    for _, h := range ignoredHosts {
        view := IgnoredHostView{IP: h.IP, Reason: h.Reason, Addresses: []IgnoredSighting{}}
        for addr, ts := range ignoredSightings {
            if ipOf(addr) == h.IP {
                view.Addresses = append(view.Addresses, IgnoredSighting{Address: addr, LastSeen: ts})
            }
        }
        sort.Slice(view.Addresses, func(i, j int) bool {
            return view.Addresses[i].LastSeen.After(view.Addresses[j].LastSeen)
        })
        out = append(out, view)
    }
    return out
}

// PurgeIgnored removes any already-known serverList entries (e.g. loaded
// from persisted state, or seeded, before a host was added to
// ignoredHosts) that now match a block. Called once at startup; new
// entries are caught going forward by checkIgnored in the discovery and
// heartbeat paths directly.
func PurgeIgnored() {
    serverMutex.Lock()
    defer serverMutex.Unlock()
    for addr := range serverList {
        if checkIgnored(addr) {
            delete(serverList, addr)
        }
    }
}
