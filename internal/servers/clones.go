package servers

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "sync"
    "time"
)

// CloneGroup records a set of addresses observed reporting identical
// hostname/map/player-roster content -- almost certainly one physical
// server broadcasting itself under multiple ports and/or IPs to inflate
// its apparent presence on the list. Rather than banning these (most
// aren't malicious, just gaming visibility, or misconfigured to register
// several times), the group is collapsed into a single displayed entry
// (Primary) with the rest recorded as AKA addresses on it.
type CloneGroup struct {
    Primary  string    `json:"primary"`
    AKA      []string  `json:"aka"`
    Reason   string    `json:"reason"`
    Detected time.Time `json:"detected"`
}

var (
    cloneGroups    = make(map[string]*CloneGroup) // primary address -> group
    aliasToPrimary = make(map[string]string)       // known alias address -> primary address
    cloneMutex     sync.Mutex
)

// isKnownAlias reports whether addr is a known alias of some other,
// canonical address -- checked by the discovery and heartbeat paths so an
// alias address never gets a second, duplicate ServerEntry once its group
// has been detected.
func isKnownAlias(addr string) bool {
    cloneMutex.Lock()
    defer cloneMutex.Unlock()
    _, ok := aliasToPrimary[addr]
    return ok
}

// matchTimeTolerance bounds how far a candidate clone's elapsed-match-time
// delta (Score_Time) is allowed to drift from the real wall-clock gap
// between when each address was polled, before pass 2 below stops
// considering it corroborating evidence. Generous enough to absorb
// second-granularity rounding in Score_Time and ordinary poll-queue
// scheduling jitter, tight enough that two genuinely independent matches
// essentially never satisfy it by chance.
const matchTimeTolerance = 8 * time.Second

// detectClones groups all known online servers by content and collapses
// any group of 2+ matching addresses into a single CloneGroup. Two passes:
//
//  1. Servers with real players: exact match on hostname, map, and sorted
//     player+bot roster. Airtight in practice -- two independent matches
//     essentially never share the same real player names.
//  2. Servers with nobody online: roster alone can't fingerprint these (two
//     coincidentally-identical *empty* default-config servers would
//     false-positive on hostname/map alone), so a match on hostname+map is
//     only trusted if every address's Q3A-family "elapsed match time"
//     (Score_Time, see MatchTimeSec/HasMatchTime) also stays in sync with
//     the real time between when each was polled -- see matchTimeTolerance.
//     ET/RTCW never report this field, so this pass only ever fires for
//     Q3A/OpenArena.
//
// Runs periodically (see StartCloneDetection); each run recomputes groups
// from current online servers and merges any newly-found addresses into
// existing groups, so a primary picked in an earlier run stays stable
// across restarts/cycles.
func detectClones() {
    type cloneKey struct {
        hostname, mapName, roster string
    }
    type hostMapKey struct {
        hostname, mapName string
    }

    rosterGroups := make(map[cloneKey][]string)
    emptyRosterAddrs := make(map[hostMapKey][]string)
    firstSeen := make(map[string]time.Time)
    lastGoodPoll := make(map[string]time.Time)
    matchTimeSec := make(map[string]int)

    serverMutex.Lock()
    for addr, s := range serverList {
        if !s.Online || s.Hostname == "" {
            continue
        }
        firstSeen[addr] = s.FirstSeen

        roster := make([]string, 0, len(s.Players)+len(s.Bots))
        roster = append(roster, s.Players...)
        roster = append(roster, s.Bots...)
        if len(roster) == 0 {
            if s.HasMatchTime {
                hk := hostMapKey{s.Hostname, s.Map}
                emptyRosterAddrs[hk] = append(emptyRosterAddrs[hk], addr)
                lastGoodPoll[addr] = s.LastGoodPoll
                matchTimeSec[addr] = s.MatchTimeSec
            }
            continue
        }
        sort.Strings(roster)

        k := cloneKey{s.Hostname, s.Map, strings.Join(roster, "\x00")}
        rosterGroups[k] = append(rosterGroups[k], addr)
    }
    serverMutex.Unlock()

    cloneMutex.Lock()

    for k, addrs := range rosterGroups {
        if len(addrs) < 2 {
            continue
        }
        reason := fmt.Sprintf(
            "Reports identical hostname %q, map %q, and player/bot roster from every address listed -- collapsed into one entry.",
            k.hostname, k.mapName,
        )
        mergeGroup(addrs, firstSeen, reason)
    }

    for hk, addrs := range emptyRosterAddrs {
        if len(addrs) < 2 {
            continue
        }
        sort.Slice(addrs, func(i, j int) bool {
            return lastGoodPoll[addrs[i]].Before(lastGoodPoll[addrs[j]])
        })
        ref := addrs[0]
        consistent := []string{ref}
        for _, a := range addrs[1:] {
            wallGap := lastGoodPoll[a].Sub(lastGoodPoll[ref])
            matchGap := time.Duration(matchTimeSec[a]-matchTimeSec[ref]) * time.Second
            diff := wallGap - matchGap
            if diff < 0 {
                diff = -diff
            }
            if diff <= matchTimeTolerance {
                consistent = append(consistent, a)
            }
        }
        if len(consistent) < 2 {
            continue
        }
        reason := fmt.Sprintf(
            "Reports identical hostname %q and map %q with nobody online, but its elapsed match-time clock (Score_Time) stays in sync with the real time between polls across every address listed -- collapsed into one entry.",
            hk.hostname, hk.mapName,
        )
        mergeGroup(consistent, firstSeen, reason)
    }

    cloneMutex.Unlock()

    applyCloneGroups()
}

// mergeGroup folds addrs (2+ addresses already confirmed to be the same
// clone) into cloneGroups, picking or reusing a stable primary. Caller
// holds cloneMutex.
func mergeGroup(addrs []string, firstSeen map[string]time.Time, reason string) {
    sort.Strings(addrs)

    primary, ok := existingPrimaryFor(addrs)
    if !ok {
        primary = addrs[0]
        for _, a := range addrs[1:] {
            if firstSeen[a].Before(firstSeen[primary]) {
                primary = a
            }
        }
    }

    group, ok := cloneGroups[primary]
    if !ok {
        group = &CloneGroup{Primary: primary}
        cloneGroups[primary] = group
    }
    akaSet := make(map[string]bool)
    for _, a := range group.AKA {
        akaSet[a] = true
    }
    for _, a := range addrs {
        if a == primary {
            continue
        }
        akaSet[a] = true
        aliasToPrimary[a] = primary
    }
    aka := make([]string, 0, len(akaSet))
    for a := range akaSet {
        aka = append(aka, a)
    }
    sort.Strings(aka)
    group.AKA = aka
    group.Reason = reason
    group.Detected = time.Now()
}

// existingPrimaryFor checks whether any address in addrs already belongs to
// a known group (as its primary or an existing alias), so repeated
// detection runs keep using the same primary rather than picking a new one
// each time. Caller holds cloneMutex.
func existingPrimaryFor(addrs []string) (string, bool) {
    for _, a := range addrs {
        if p, ok := aliasToPrimary[a]; ok {
            return p, true
        }
        if _, ok := cloneGroups[a]; ok {
            return a, true
        }
    }
    return "", false
}

// applyCloneGroups stamps each primary's ServerEntry with its current AKA
// list and removes every alias address's own entry from serverList, so the
// main list and network totals count the group once instead of N times.
func applyCloneGroups() {
    cloneMutex.Lock()
    defer cloneMutex.Unlock()

    serverMutex.Lock()
    defer serverMutex.Unlock()
    for primary, group := range cloneGroups {
        if entry, ok := serverList[primary]; ok {
            entry.AlsoKnownAs = group.AKA
        }
        for _, alias := range group.AKA {
            delete(serverList, alias)
        }
    }
}

// StartCloneDetection runs detectClones immediately and then every
// interval, persisting the current groups to persistPath after each run so
// known aliases survive restarts instead of needing to be rediscovered.
func StartCloneDetection(interval time.Duration, persistPath string) {
    go func() {
        for {
            detectClones()
            if err := SaveCloneGroups(persistPath); err != nil {
                fmt.Printf("failed to persist clone groups: %v\n", err)
            }
            time.Sleep(interval)
        }
    }()
}

// SaveCloneGroups/LoadCloneGroups persist detected clone groups to their
// own file, deliberately separate from the main server state file, so
// loading one can't be broken by a format change in the other.
func SaveCloneGroups(path string) error {
    cloneMutex.Lock()
    snapshot := make([]*CloneGroup, 0, len(cloneGroups))
    for _, g := range cloneGroups {
        snapshot = append(snapshot, g)
    }
    cloneMutex.Unlock()

    sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Primary < snapshot[j].Primary })

    data, err := json.MarshalIndent(snapshot, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal clone groups: %w", err)
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return fmt.Errorf("ensure clone groups dir: %w", err)
    }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil {
        return fmt.Errorf("write temp clone groups file: %w", err)
    }
    return os.Rename(tmp, path)
}

// LoadCloneGroups loads previously-persisted clone groups. It is not an
// error for the file to be missing (nothing detected yet).
func LoadCloneGroups(path string) error {
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return fmt.Errorf("read clone groups file: %w", err)
    }
    var loaded []*CloneGroup
    if err := json.Unmarshal(data, &loaded); err != nil {
        return fmt.Errorf("unmarshal clone groups file: %w", err)
    }
    cloneMutex.Lock()
    for _, g := range loaded {
        cloneGroups[g.Primary] = g
        for _, a := range g.AKA {
            aliasToPrimary[a] = g.Primary
        }
    }
    cloneMutex.Unlock()
    return nil
}
