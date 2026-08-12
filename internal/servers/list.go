package servers

import (
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

