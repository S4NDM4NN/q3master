package servers

import "time"

// StartJanitor runs periodic cleanup and state reconciliation.
func StartJanitor() {
    ticker := time.NewTicker(1 * time.Minute)
    go func() {
        for range ticker.C {
            now := time.Now()

            serverMutex.Lock()
            for addr, s := range serverList {
                // Keep Online bool in sync with State
                s.Online = (s.State == StateOnline)

                switch s.State {
                case StateNew:
                    // New servers fall off after 10 missed polls
                    if s.MissedPolls >= 10 {
                        delete(serverList, addr)
                    }
                case StateOffline:
                    // Offline servers fall off after 7 days since last good poll
                    if !s.LastGoodPoll.IsZero() && now.Sub(s.LastGoodPoll) >= 7*24*time.Hour {
                        delete(serverList, addr)
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
        }
    }()
}

