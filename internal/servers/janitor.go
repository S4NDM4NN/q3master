package servers

import "time"

// StartJanitor runs periodic cleanup and state reconciliation.
func StartJanitor() {
    ticker := time.NewTicker(1 * time.Minute)
    go func() {
        for range ticker.C {
            now := time.Now()
            var evicted []string

            serverMutex.Lock()
            for addr, s := range serverList {
                // Keep Online bool in sync with State
                s.Online = (s.State == StateOnline)

                switch s.State {
                case StateNew:
                    // New servers fall off after 10 missed polls
                    if s.MissedPolls >= 10 {
                        delete(serverList, addr)
                        evicted = append(evicted, addr)
                    }
                case StateOffline:
                    // Offline servers fall off after 24 hours since last good poll
                    if !s.LastGoodPoll.IsZero() && now.Sub(s.LastGoodPoll) >= 24*time.Hour {
                        delete(serverList, addr)
                        evicted = append(evicted, addr)
                    }
                case StateOnline:
                    // no eviction; they remain as long as they keep polling
                    if !s.LastSeen.IsZero() && now.Sub(s.LastSeen) > 5*time.Minute {
                        s.State = StateOffline
                        s.Online = false
                    }
                }
            }
            serverMutex.Unlock()

            // releaseOrphanedGroups needs cloneMutex, then (if it finds
            // anything to restore) serverMutex again -- called after fully
            // releasing serverMutex above, never nested inside it, to keep
            // the established lock order (cloneMutex before serverMutex,
            // see applyCloneGroups/GetPortPaddingGroups) intact.
            releaseOrphanedGroups(evicted)
        }
    }()
}

