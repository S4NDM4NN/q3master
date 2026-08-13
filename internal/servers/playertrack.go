package servers

import (
    "sort"
    "sync"
    "time"
)

// suspectedBotSessionThreshold is how long a name must have been
// continuously present in a server's *real*-player roster (never dropping
// out, not even for one poll) before it's flagged as a suspected bot
// spoofing itself as a real player rather than reporting honestly as a
// bot (ping 0). Ordinary human play sessions run minutes to a few hours;
// even a genuinely AFK human rarely stays connected uninterrupted this
// long, while a roster entry that's actually just padding never
// disconnects at all and clears it easily. Deliberately won't catch every
// spoofed bot -- one that happens to "leave" every so often (resetting its
// own clock) slips through -- but should catch the common case: a fixed
// roster of fake names that's simply always there.
const suspectedBotSessionThreshold = 8 * time.Hour

var (
    // playerSessionStart tracks, for each server address, when each
    // currently-listed real player was first observed in their current
    // unbroken session. A name missing from one poll's roster and
    // reappearing later gets a fresh start time -- exactly the
    // leave-and-rejoin behavior a genuine human exhibits, and exactly what
    // a spoofed, always-present entry doesn't.
    playerSessionStart = make(map[string]map[string]time.Time)
    playerTrackMutex    sync.Mutex
)

// updatePlayerSessions records/refreshes session-start times for addr's
// current real-player roster and returns the subset of names that have
// been continuously present past suspectedBotSessionThreshold. Called once
// per successful poll, with the server's freshly-parsed Players list (not
// Bots -- those already self-report honestly and don't need this).
func updatePlayerSessions(addr string, players []string) []string {
    now := time.Now()
    playerTrackMutex.Lock()
    defer playerTrackMutex.Unlock()

    sessions, ok := playerSessionStart[addr]
    if !ok {
        sessions = make(map[string]time.Time)
        playerSessionStart[addr] = sessions
    }

    current := make(map[string]bool, len(players))
    var suspected []string
    for _, name := range players {
        current[name] = true
        start, seen := sessions[name]
        if !seen {
            sessions[name] = now
            continue
        }
        if now.Sub(start) >= suspectedBotSessionThreshold {
            suspected = append(suspected, name)
        }
    }
    // Anyone no longer in the roster stops being tracked -- if they show
    // up again later it's a fresh session, not a continuation.
    for name := range sessions {
        if !current[name] {
            delete(sessions, name)
        }
    }

    sort.Strings(suspected)
    return suspected
}

// clearPlayerSessions drops all tracked session state for addr. Called
// when a server actually goes offline: with no way to observe what
// happened to its roster while unreachable, treating anyone still listed
// once it comes back as having been continuously present the whole time
// would be a guess, not an observation.
func clearPlayerSessions(addr string) {
    playerTrackMutex.Lock()
    delete(playerSessionStart, addr)
    playerTrackMutex.Unlock()
}
