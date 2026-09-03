package servers

import (
    "sort"
    "strconv"
    "time"
)

// ListServers returns a snapshot slice of all servers.
func ListServers() []*ServerEntry {
    serverMutex.Lock()
    defer serverMutex.Unlock()

    list := make([]*ServerEntry, 0, len(serverList))
    for _, s := range serverList {
        list = append(list, s)
    }
    return list
}

// ServerSummary is the minimal per-server payload for the server-browser
// list view (see ServeServersSummaryAPI) -- every field the list needs to
// render cards, filter, and sort, without the heavier ones (full
// player/bot rosters, poll-source history, missed-poll count) that are only
// needed once a specific server's detail view is opened, via GetServer /
// ServeServerAPI. Those heavier fields are exactly what made the full
// /api/servers payload multiple megabytes once the tracked list grew past a
// few thousand entries -- most of it for servers the list is showing
// collapsed anyway.
type ServerSummary struct {
    Address       string      `json:"address"`
    Hostname      string      `json:"hostname"`
    Map           string      `json:"map"`
    Mod           string      `json:"mod"`
    GameType      string      `json:"gametype"`
    Version       string      `json:"version"`
    Protocol      int         `json:"protocol"`
    PlayerCount   int         `json:"player_count"`
    MaxPlayers    int         `json:"max_players"`
    BotCount      int         `json:"bot_count"`
    Online        bool        `json:"online"`
    State         ServerState `json:"state"`
    FirstSeen     time.Time   `json:"first_seen"`
    LastSeen      time.Time   `json:"last_seen"`
    LastHeartbeat time.Time   `json:"last_heartbeat"`
    AlsoKnownAs   []AKAEntry  `json:"also_known_as,omitempty"`
}

// ListServerSummaries returns a snapshot of every server as a
// ServerSummary -- see that type for why it's a separate, lighter shape
// from ListServers.
func ListServerSummaries() []ServerSummary {
    serverMutex.Lock()
    defer serverMutex.Unlock()

    list := make([]ServerSummary, 0, len(serverList))
    for _, s := range serverList {
        list = append(list, ServerSummary{
            Address:       s.Address,
            Hostname:      s.Hostname,
            Map:           s.Map,
            Mod:           s.Mod,
            GameType:      s.GameType,
            Version:       s.Version,
            Protocol:      s.Protocol,
            PlayerCount:   s.PlayerCount,
            MaxPlayers:    s.MaxPlayers,
            BotCount:      s.BotCount,
            Online:        s.Online,
            State:         s.State,
            FirstSeen:     s.FirstSeen,
            LastSeen:      s.LastSeen,
            LastHeartbeat: s.LastHeartbeat,
            AlsoKnownAs:   s.AlsoKnownAs,
        })
    }
    return list
}

// GetServer returns a snapshot of a single server entry by address, and
// whether it was found.
func GetServer(address string) (*ServerEntry, bool) {
    serverMutex.Lock()
    defer serverMutex.Unlock()

    s, ok := serverList[address]
    if !ok {
        return nil, false
    }
    entryCopy := *s
    return &entryCopy, true
}

// SourceCount is how many currently-tracked servers one source (a known
// master's label, or the direct-heartbeat pseudo-source) has reported --
// part of GetSourceCounts.
type SourceCount struct {
    Label        string `json:"label"`
    KnownServers int    `json:"known_servers"`
}

// GetSourceCounts returns how many currently-tracked servers (everything in
// serverList, online or not) each source has reported, across every label
// actually seen on a ServerEntry.Sources map -- both real masters (see
// knownMasters) and the direct-heartbeat pseudo-source -- rather than being
// hardcoded to knownMasters, so it stays accurate even if that list changes.
// A server counts under every source that's ever reported it, so these
// don't sum to the total server count; sorted by count descending, then
// label for a stable order.
func GetSourceCounts() []SourceCount {
    counts := make(map[string]int)

    serverMutex.Lock()
    for _, s := range serverList {
        for label := range s.Sources {
            counts[label]++
        }
    }
    serverMutex.Unlock()

    out := make([]SourceCount, 0, len(counts))
    for label, n := range counts {
        out = append(out, SourceCount{Label: label, KnownServers: n})
    }
    sort.Slice(out, func(i, j int) bool {
        if out[i].KnownServers != out[j].KnownServers {
            return out[i].KnownServers > out[j].KnownServers
        }
        return out[i].Label < out[j].Label
    })
    return out
}

// ProtocolSummary is the total real player count (bots excluded, since
// PlayerCount already only counts non-bot players) and number of online
// servers for one protocol bucket.
type ProtocolSummary struct {
    TotalPlayers   int
    OnlineServers int
}

// SummarizeOnlineByProtocol returns online-fleet totals broken down by
// protocol (keyed by the protocol number as a string, e.g. "84"), plus an
// "all" bucket summing across every protocol.
func SummarizeOnlineByProtocol() map[string]ProtocolSummary {
    serverMutex.Lock()
    defer serverMutex.Unlock()

    result := make(map[string]ProtocolSummary)
    all := ProtocolSummary{}
    for _, s := range serverList {
        if !s.Online {
            continue
        }
        all.TotalPlayers += s.PlayerCount
        all.OnlineServers++

        key := strconv.Itoa(s.Protocol)
        ps := result[key]
        ps.TotalPlayers += s.PlayerCount
        ps.OnlineServers++
        result[key] = ps
    }
    result["all"] = all
    return result
}

// StartNetworkSampling periodically calls record once per protocol bucket
// (including "all") with the current total real player count and online
// server count, independent of any single server's poll cadence. Callers
// that don't want network-wide sampling (e.g. history tracking disabled)
// can just not call this.
func StartNetworkSampling(interval time.Duration, record func(protocol string, totalPlayers, onlineServers int)) {
    go func() {
        for {
            time.Sleep(interval)
            for protocol, summary := range SummarizeOnlineByProtocol() {
                record(protocol, summary.TotalPlayers, summary.OnlineServers)
            }
        }
    }()
}

