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
    Primary  string     `json:"primary"`
    AKA      []AKAEntry `json:"aka"`
    Reason   string     `json:"reason"`
    Detected time.Time  `json:"detected"`
}

// AKAEntry names one address folded into a CloneGroup, with the hostname
// text it was reporting at the moment it was folded in. Alias addresses
// are never independently polled again afterward (see isKnownAlias), so
// this is a one-time snapshot, not a live value -- still useful context,
// e.g. distinguishing a region-labeled mirror ("...eu" vs "...de") of the
// same live match from a truly anonymous duplicate port.
type AKAEntry struct {
    Address  string `json:"address"`
    Hostname string `json:"hostname"`
    // Protocol is captured alongside Hostname (same one-time-snapshot
    // caveat) so historical-chart correction (see AliasAddresses and
    // internal/history's use of it) can scope which protocol bucket an
    // alias's already-recorded contribution should be subtracted from.
    Protocol int `json:"protocol"`
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
// any group of 2+ matching addresses into a single CloneGroup. Three
// tiers, by how much signal is actually available:
//
//  1. At least one real (non-bot) player: match on map and sorted
//     player+bot roster -- hostname is deliberately NOT part of the key
//     here. A real player's chosen nickname colliding by chance across two
//     genuinely independent servers is astronomically unlikely, so the
//     roster alone is airtight; requiring hostname too would just let an
//     operator dodge detection with a cosmetic hostname variation (found
//     2026-08-13: the same live match broadcasting as "...fpsclasico.de"
//     on one address and "...fpsclasico.eu" on another -- identical
//     roster/scores/map, hostname the only thing different).
//  2. Bots but no real players: match on hostname + map + bot roster
//     (all three, exact). Bot names alone are drawn from a small canonical
//     default pool (Sarge, Grunt, ...), so two coincidentally-identical
//     all-bot servers could exist on a popular default map/hostname -- but
//     requiring the operator's own (usually distinctive) hostname text to
//     match too, on top of the specific bot combination, rules that out in
//     practice (found 2026-08-13: an "Artifex {FFA}" network with 2 bots,
//     which this tier now correctly catches -- it doesn't send Score_Time
//     at all, so tier 3 alone was missing it).
//  3. No roster at all (nothing online, not even bots): the weakest case,
//     nothing to fingerprint by except hostname+map, so this tier only
//     trusts a match if every address's Q3A-family "elapsed match time"
//     (Score_Time, see MatchTimeSec/HasMatchTime) also stays in sync with
//     the real time between when each was polled -- see
//     matchTimeTolerance. ET/RTCW never report Score_Time, so this tier
//     only ever fires for Q3A/OpenArena.
//
// Runs periodically (see StartCloneDetection); each run recomputes groups
// from current online servers and merges any newly-found addresses into
// existing groups, so a primary picked in an earlier run stays stable
// across restarts/cycles.
func detectClones() {
    type cloneKey struct {
        mapName, roster string
    }
    type hostRosterKey struct {
        hostname, mapName, roster string
    }
    type hostMapKey struct {
        hostname, mapName string
    }

    rosterGroups := make(map[cloneKey][]string)
    botRosterGroups := make(map[hostRosterKey][]string)
    emptyGroups := make(map[hostMapKey][]string)
    firstSeen := make(map[string]time.Time)
    lastGoodPoll := make(map[string]time.Time)
    matchTimeSec := make(map[string]int)
    hostnameOf := make(map[string]string)
    protocolOf := make(map[string]int)

    serverMutex.Lock()
    for addr, s := range serverList {
        if !s.Online || s.Hostname == "" {
            continue
        }
        firstSeen[addr] = s.FirstSeen
        hostnameOf[addr] = s.Hostname
        protocolOf[addr] = s.Protocol

        roster := make([]string, 0, len(s.Players)+len(s.Bots))
        roster = append(roster, s.Players...)
        roster = append(roster, s.Bots...)
        sort.Strings(roster)

        switch {
        case len(s.Players) > 0:
            k := cloneKey{s.Map, strings.Join(roster, "\x00")}
            rosterGroups[k] = append(rosterGroups[k], addr)
        case len(roster) > 0: // bots only
            k := hostRosterKey{s.Hostname, s.Map, strings.Join(roster, "\x00")}
            botRosterGroups[k] = append(botRosterGroups[k], addr)
        case s.HasMatchTime: // nothing online at all
            hk := hostMapKey{s.Hostname, s.Map}
            emptyGroups[hk] = append(emptyGroups[hk], addr)
            lastGoodPoll[addr] = s.LastGoodPoll
            matchTimeSec[addr] = s.MatchTimeSec
        }
    }
    serverMutex.Unlock()

    cloneMutex.Lock()

    for k, addrs := range rosterGroups {
        if len(addrs) < 2 {
            continue
        }
        reason := fmt.Sprintf(
            "Reports identical map %q and player/bot roster from every address listed -- collapsed into one entry.",
            k.mapName,
        )
        mergeGroup(addrs, firstSeen, hostnameOf, protocolOf, reason)
    }

    for k, addrs := range botRosterGroups {
        if len(addrs) < 2 {
            continue
        }
        reason := fmt.Sprintf(
            "Reports identical hostname %q, map %q, and bot roster (no real players online) from every address listed -- collapsed into one entry.",
            k.hostname, k.mapName,
        )
        mergeGroup(addrs, firstSeen, hostnameOf, protocolOf, reason)
    }

    for hk, addrs := range emptyGroups {
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
            "Reports identical hostname %q and map %q with nobody (not even bots) online, but its elapsed match-time clock (Score_Time) stays in sync with the real time between polls across every address listed -- collapsed into one entry.",
            hk.hostname, hk.mapName,
        )
        mergeGroup(consistent, firstSeen, hostnameOf, protocolOf, reason)
    }

    cloneMutex.Unlock()

    applyCloneGroups()
}

// mergeGroup folds addrs (2+ addresses already confirmed to be the same
// clone) into cloneGroups, picking or reusing a stable primary, and
// records each alias's hostname at the moment it was folded in (aliases
// are never independently polled again afterward, so this is a one-time
// snapshot -- see AKAEntry). Caller holds cloneMutex.
func mergeGroup(addrs []string, firstSeen map[string]time.Time, hostnameOf map[string]string, protocolOf map[string]int, reason string) {
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
    akaMap := make(map[string]AKAEntry, len(group.AKA))
    for _, e := range group.AKA {
        akaMap[e.Address] = e
    }
    for _, a := range addrs {
        if a == primary {
            continue
        }
        akaMap[a] = AKAEntry{Address: a, Hostname: hostnameOf[a], Protocol: protocolOf[a]}
        aliasToPrimary[a] = primary
    }
    aka := make([]AKAEntry, 0, len(akaMap))
    for _, e := range akaMap {
        aka = append(aka, e)
    }
    sort.Slice(aka, func(i, j int) bool { return aka[i].Address < aka[j].Address })
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
            delete(serverList, alias.Address)
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
            aliasToPrimary[a.Address] = g.Primary
        }
    }
    cloneMutex.Unlock()
    return nil
}

// AliasAddresses returns every address currently known to be a clone-
// detected alias (folded into some other server -- see CloneGroup), keyed
// by address with the protocol it was reporting when folded in. This
// package already imports internal/history (RecordSample), so history
// can't import it back to ask directly; callers that need both (see
// httpapi.ServeNetworkHistoryAPI) fetch this and pass it into history's
// query functions, which use it to subtract each alias's own historical
// contribution from network-total charts that were recorded before the
// alias was known to be a duplicate.
func AliasAddresses() map[string]int {
    cloneMutex.Lock()
    defer cloneMutex.Unlock()
    out := make(map[string]int, len(aliasToPrimary))
    for _, g := range cloneGroups {
        for _, a := range g.AKA {
            out[a.Address] = a.Protocol
        }
    }
    return out
}
