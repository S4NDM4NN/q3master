package servers

import "time"

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

// SummarizeOnline returns the total real player count (bots excluded, since
// PlayerCount already only counts non-bot players) and the number of online
// servers, across the whole tracked fleet.
func SummarizeOnline() (totalPlayers, onlineServers int) {
    serverMutex.Lock()
    defer serverMutex.Unlock()

    for _, s := range serverList {
        if s.Online {
            totalPlayers += s.PlayerCount
            onlineServers++
        }
    }
    return totalPlayers, onlineServers
}

// StartNetworkSampling periodically calls record with the current total real
// player count and online server count, independent of any single server's
// poll cadence. Callers that don't want network-wide sampling (e.g. history
// tracking disabled) can just not call this.
func StartNetworkSampling(interval time.Duration, record func(totalPlayers, onlineServers int)) {
    go func() {
        for {
            time.Sleep(interval)
            totalPlayers, onlineServers := SummarizeOnline()
            record(totalPlayers, onlineServers)
        }
    }()
}

