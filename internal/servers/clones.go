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
// text it was reporting at the moment it was folded in. Alias addresses are
// excluded from ordinary discovery/heartbeat/polling for as long as they
// stay paired (see isKnownAlias), so Hostname/Protocol are a one-time
// snapshot, not a live value -- still useful context, e.g. distinguishing a
// region-labeled mirror ("...eu" vs "...de") of the same live match from a
// truly anonymous duplicate port. They ARE periodically re-verified with a
// direct poll of their own, though -- see clone_recheck.go -- which is what
// FirstPaired/LastChecked/CheckCount below track; a pairing that no longer
// holds up gets unpaired (clone_recheck.go's unpairAlias) rather than
// staying wrong forever.
type AKAEntry struct {
    Address  string `json:"address"`
    Hostname string `json:"hostname"`
    // Protocol is captured alongside Hostname (same one-time-snapshot
    // caveat) so historical-chart correction (see AliasAddresses and
    // internal/history's use of it) can scope which protocol bucket an
    // alias's already-recorded contribution should be subtracted from.
    Protocol int `json:"protocol"`
    // FirstPaired is when this address was first folded into its group --
    // set once by mergeGroup and never touched again, so it's a stable
    // "how long has this pairing held up" anchor even though CloneGroup's
    // own Detected keeps moving as the group gains other members.
    FirstPaired time.Time `json:"first_paired"`
    // LastChecked is when clone_recheck.go last directly re-polled this
    // alias and compared it against its primary's current content -- zero
    // if it's never been rechecked yet (due for its first recheck
    // recheckIntervalLadder[0] after FirstPaired).
    LastChecked time.Time `json:"last_checked,omitempty"`
    // CheckCount is how many *consecutive* rechecks have confirmed the
    // pairing still holds, without a single contradiction in between --
    // indexes into recheckIntervalLadder (clone_recheck.go), so it's what
    // actually slows a mature, repeatedly-confirmed pairing down toward the
    // once-a-day floor. A no-response recheck (see recheckOne) leaves this
    // alone rather than resetting or advancing it -- aliases on a busy
    // relay have long dead stretches routinely, and a single missed poll
    // isn't evidence either way.
    CheckCount int `json:"check_count"`
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

// realRosterMinSize/realRosterMaxDrift bound the match used for tier 1
// below: two real-player rosters on the same map are treated as the same
// clone if they differ by at most realRosterMaxDrift names (0 meaning
// byte-identical) while both having at least realRosterMinSize names total.
// The drift tolerance absorbs ordinary player churn between when different
// addresses get polled (found 2026-08-13: two aliases of the same busy A51
// server differed by exactly one swapped player -- 15 of 16 names
// identical -- and so never matched under a byte-exact comparison, no
// matter how long detection kept running) without opening the door to
// coincidence: two genuinely independent, decently-populated matches
// essentially never share all but one or two of their real players' exact
// chosen nicknames.
//
// Raised from 2 to 4 on 2026-08-16: the same A51 network kept showing up
// split across multiple separate clone groups on the port-padding dashboard
// -- one concrete case, "*A51* FFA" on 172.104.253.108, had 172.104.253.108:
// 32026 and :32036 both online with 14 of their 16 real players identical
// (a symmetric diff of 4) but staying two unrelated-looking entries because
// the old tolerance of 2 rejected them. A second, larger case spanned 5
// separate clone groups (138 total addresses) for what's evidently one
// physical relay across 4 IPs. Still gated by realRosterMinSize=6 on both
// sides, so the coincidence risk this guards against doesn't meaningfully
// change: a stranger matching 12+ of 16 real chosen nicknames by chance is
// no more plausible than matching 14+ of 16 was.
//
// The size floor applies even to a byte-identical (0-drift) match -- it
// used to only gate the fuzzy path, which meant two totally unrelated
// servers that each happened to have exactly one real player who'd never
// set a name (both defaulting to the client's generic placeholder, e.g.
// "UnnamedPlayer") could satisfy an exact-roster match by coincidence on a
// popular map. Found 2026-08-16: 213.202.230.213:27967's clone group had
// swept in 51.254.137.97:27960 ("Wait & Bleed - OSP [Q3 1.32e]") this way --
// an unrelated server with no hostname resemblance to the group's other,
// genuine members, joined solely because both had a lone "UnnamedPlayer".
// A single matching name (chosen or not) is nowhere near "astronomically
// unlikely" the way a whole populated roster matching is.
const (
    realRosterMinSize  = 6
    realRosterMaxDrift = 4
)

// detectClones groups all known online servers by content and collapses
// any group of 2+ matching addresses into a single CloneGroup. Three
// tiers, by how much signal is actually available:
//
//  1. At least one real (non-bot) player: match on map and player+bot
//     roster, fuzzily (see realRosterMinSize/realRosterMaxDrift) -- hostname
//     is deliberately NOT part of the key here. A real player's chosen
//     nickname colliding by chance across two genuinely independent servers
//     is astronomically unlikely, so the roster alone is airtight;
//     requiring hostname too would just let an operator dodge detection
//     with a cosmetic hostname variation (found 2026-08-13: the same live
//     match broadcasting as "...fpsclasico.de" on one address and
//     "...fpsclasico.eu" on another -- identical roster/scores/map,
//     hostname the only thing different).
//  2. Bots but no real players: match on hostname + map + bot roster
//     (all three, exact -- no fuzz here, bot rosters don't churn the way
//     real players do). Bot names alone are drawn from a small canonical
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
    type realEntry struct {
        addr    string
        mapName string
        roster  map[string]bool
    }
    type hostRosterKey struct {
        hostname, mapName, roster string
    }
    type hostMapKey struct {
        hostname, mapName string
    }

    var realEntries []realEntry
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

        switch {
        case len(s.Players) > 0:
            rosterSet := make(map[string]bool, len(roster))
            for _, n := range roster {
                rosterSet[n] = true
            }
            realEntries = append(realEntries, realEntry{addr: addr, mapName: s.Map, roster: rosterSet})
        case len(roster) > 0: // bots only
            sort.Strings(roster)
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

    // Tier 1: bucket by map (cheap, still a hard requirement), then union
    // same-map entries whose rosters are identical or near-identical.
    byMap := make(map[string][]int)
    for i, e := range realEntries {
        byMap[e.mapName] = append(byMap[e.mapName], i)
    }
    uf := newUnionFind(len(realEntries))
    for _, idxs := range byMap {
        for a := 0; a < len(idxs); a++ {
            for b := a + 1; b < len(idxs); b++ {
                i, j := idxs[a], idxs[b]
                diff := rosterSymmetricDiff(realEntries[i].roster, realEntries[j].roster)
                if diff <= realRosterMaxDrift &&
                    len(realEntries[i].roster) >= realRosterMinSize &&
                    len(realEntries[j].roster) >= realRosterMinSize {
                    uf.union(i, j)
                }
            }
        }
    }
    components := make(map[int][]int)
    for i := range realEntries {
        root := uf.find(i)
        components[root] = append(components[root], i)
    }
    for _, idxs := range components {
        if len(idxs) < 2 {
            continue
        }
        addrs := make([]string, len(idxs))
        for k, idx := range idxs {
            addrs[k] = realEntries[idx].addr
        }
        reason := fmt.Sprintf(
            "Reports identical (or near-identical, allowing for ordinary player churn between polls) map %q and player/bot roster from every address listed -- collapsed into one entry.",
            realEntries[idxs[0]].mapName,
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
// clone) into cloneGroups, picking or reusing a stable primary, and records
// each alias's hostname at the moment it was folded in (a one-time
// snapshot -- see AKAEntry). If addrs happens to include more than one
// address that was already its own group's primary (two previously-
// separate fragments finally matching each other), the losing primary's
// own group is absorbed transitively rather than left behind as an orphan
// -- see the loop below. Caller holds cloneMutex.
//
// An address already in aliasToPrimary can never appear in addrs here --
// detectClones only ever considers addresses currently in serverList, and
// an already-paired alias was removed from it (applyCloneGroups) -- except
// after clone_recheck.go's unpairAlias restores one, which is exactly why
// this treats every address in addrs as a fresh pairing (FirstPaired reset
// to now) rather than trying to preserve old recheck history across an
// unpair/re-pair cycle.
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
        akaMap[a] = AKAEntry{Address: a, Hostname: hostnameOf[a], Protocol: protocolOf[a], FirstPaired: time.Now()}
        aliasToPrimary[a] = primary

        // a may itself have been an existing primary with its own group --
        // two previously-separate fragments finally recognized as the same
        // operation in this run (existingPrimaryFor above only keeps ONE of
        // them as the surviving primary). Absorb the loser's aliases
        // transitively instead of leaving its group behind as an instant
        // orphan the instant a's own ServerEntry gets deleted (via
        // applyCloneGroups, since a is now listed as primary's alias).
        // Found 2026-08-16: 172.104.253.108:32039 had its own 15-alias
        // group; once detectClones finally matched it against
        // 172.104.253.108:32026's bigger group, 32039 folded in here as a
        // plain new alias but its old group was never touched -- orphaning
        // it and all 15 of ITS aliases within one detection cycle. Not
        // caught by releaseOrphanedGroups (janitor.go): that only triggers
        // on eviction, and 32039 was never evicted, just absorbed.
        if oldGroup, wasPrimary := cloneGroups[a]; wasPrimary {
            for _, oa := range oldGroup.AKA {
                // Preserve FirstPaired/LastChecked/CheckCount -- these were
                // already independently verified against a, and a is now
                // confirmed to be the same as primary, so their trust
                // history is still valid; this isn't a fresh pairing the
                // way clone_recheck.go's unpair/re-pair is.
                akaMap[oa.Address] = oa
                aliasToPrimary[oa.Address] = primary
            }
            delete(cloneGroups, a)
        }
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

// rosterSymmetricDiff counts names present in exactly one of the two
// rosters -- 0 for byte-identical, small for a handful of players
// joining/leaving between when each address was polled.
func rosterSymmetricDiff(a, b map[string]bool) int {
    diff := 0
    for name := range a {
        if !b[name] {
            diff++
        }
    }
    for name := range b {
        if !a[name] {
            diff++
        }
    }
    return diff
}

// unionFind is a minimal disjoint-set structure (path-compressed finds,
// unranked unions -- the tiny input sizes here don't need union-by-rank)
// used to cluster tier 1's same-map real-player entries into connected
// components under rosterSymmetricDiff, so near-matches chain together
// (A~B~C) even if the drift accumulates beyond realRosterMaxDrift between
// the two most-different members of a larger group.
type unionFind struct {
    parent []int
}

func newUnionFind(n int) *unionFind {
    uf := &unionFind{parent: make([]int, n)}
    for i := range uf.parent {
        uf.parent[i] = i
    }
    return uf
}

func (uf *unionFind) find(x int) int {
    for uf.parent[x] != x {
        uf.parent[x] = uf.parent[uf.parent[x]]
        x = uf.parent[x]
    }
    return x
}

func (uf *unionFind) union(a, b int) {
    ra, rb := uf.find(a), uf.find(b)
    if ra != rb {
        uf.parent[ra] = rb
    }
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

// RebuildCloneGroupsFromServerState reconstructs cloneGroups/aliasToPrimary
// from any AlsoKnownAs already present on loaded ServerEntry values.
//
// LoadState/LoadOrSeed (persistence.go) populate serverList -- including
// AlsoKnownAs, since it's just another field on ServerEntry's JSON schema --
// straight from a state file or a remote instance's /api/servers response.
// Neither touches cloneGroups/aliasToPrimary, which live in their own file
// (clone_groups.json) that a freshly-seeded instance (see SERVER_SEED_URL)
// never had. Without this, a seeded instance shows padding badges on cards
// (AlsoKnownAs came along in the seed's JSON) while GetPortPaddingGroups --
// which reads cloneGroups -- sees nothing, isKnownAlias never blocks the
// alias addresses from re-entering, and the poller/getservers responder keep
// treating them as independent entries: the exact "server shows padding on
// the card but isn't on the dashboard, and its aliases are still being
// served too" symptom found 2026-08-16 on a PR preview deploy, which -- with
// no local clone_groups.json yet -- seeds entirely from production's already-
// collapsed /api/servers.
//
// Called once at startup after server state is loaded (LoadOrSeed) and
// before PurgeIgnored; a no-op past that point since applyCloneGroups keeps
// deleting alias entries as they're found, so there's nothing left to scan
// for on any later call.
func RebuildCloneGroupsFromServerState() {
    serverMutex.Lock()
    type seed struct {
        primary string
        aka     []AKAEntry
    }
    var seeds []seed
    for addr, s := range serverList {
        if len(s.AlsoKnownAs) > 0 {
            seeds = append(seeds, seed{primary: addr, aka: s.AlsoKnownAs})
        }
    }
    serverMutex.Unlock()

    if len(seeds) == 0 {
        return
    }

    cloneMutex.Lock()
    for _, sd := range seeds {
        if _, exists := cloneGroups[sd.primary]; exists {
            continue // already known, e.g. loaded from clone_groups.json
        }
        cloneGroups[sd.primary] = &CloneGroup{
            Primary:  sd.primary,
            AKA:      sd.aka,
            Reason:   "Inherited from loaded/seeded server state -- the original detection reason wasn't preserved across the load boundary.",
            Detected: time.Now(),
        }
        for _, a := range sd.aka {
            if _, known := aliasToPrimary[a.Address]; !known {
                aliasToPrimary[a.Address] = sd.primary
            }
        }
    }
    cloneMutex.Unlock()

    // Purge any alias addresses that leaked in as their own top-level
    // entries (possible for every seed/load this reconstructs, since
    // whatever loaded them had no aliasToPrimary to block them either).
    applyCloneGroups()
}

// releaseOrphanedGroups checks each of evicted (addresses the janitor just
// removed from serverList) against cloneGroups: if one was a clone group's
// primary, the whole group is now orphaned. Without this, it would sit on
// the port-padding dashboard forever showing a blank hostname and "offline"
// (GetPortPaddingGroups falls back gracefully when the primary is missing
// from serverList, which is what makes an orphan look plausible instead of
// broken) while every one of its aliases stays permanently excluded from
// discovery/polling/serving via isKnownAlias, even though nothing is
// verifying them anymore -- clone_recheck.go's scheduled/manual recheck
// both skip an alias whose primary isn't currently online, which "primary
// doesn't exist at all" satisfies just as much as "temporarily down" does,
// so recheck alone can never recover this case. Found 2026-08-16: 4 of 5
// fragments of one A51 network had already lost their primary this way.
//
// Releases the group: deletes the CloneGroup record and restores each
// alias as an independent, freshly pollable ServerEntry -- exactly like
// unpairAlias (clone_recheck.go), just triggered by the primary
// disappearing instead of a content mismatch on recheck.
func releaseOrphanedGroups(evicted []string) {
    if len(evicted) == 0 {
        return
    }

    var toRestore []AKAEntry

    cloneMutex.Lock()
    for _, primary := range evicted {
        g, ok := cloneGroups[primary]
        if !ok {
            continue
        }
        for _, a := range g.AKA {
            delete(aliasToPrimary, a.Address)
        }
        toRestore = append(toRestore, g.AKA...)
        delete(cloneGroups, primary)
    }
    cloneMutex.Unlock()

    if len(toRestore) == 0 {
        return
    }

    now := time.Now()
    serverMutex.Lock()
    for _, a := range toRestore {
        if _, exists := serverList[a.Address]; exists {
            continue // shouldn't happen (isKnownAlias blocked it until just now), but don't clobber if it does
        }
        serverList[a.Address] = &ServerEntry{
            Address:     a.Address,
            Protocol:    a.Protocol,
            Hostname:    a.Hostname,
            State:       StateNew,
            FirstSeen:   now,
            LastAttempt: time.Time{},
        }
    }
    serverMutex.Unlock()

    for _, a := range toRestore {
        EnqueuePoll(a.Address)
    }

    fmt.Printf("released %d alias(es) from %d orphaned clone group(s) (primary evicted)\n", len(toRestore), len(evicted))
}

// ReleaseAlreadyOrphanedGroups scans every known clone group for a primary
// that's already missing from serverList and releases it (see
// releaseOrphanedGroups) -- e.g. evicted before this cleanup existed, or
// between one run's eviction and the next restart. StartJanitor's own call
// to releaseOrphanedGroups only ever sees primaries *it* evicts from that
// point on; this is what catches everything already orphaned before that.
// Call once at startup, after server state is loaded/reconstructed.
func ReleaseAlreadyOrphanedGroups() {
    cloneMutex.Lock()
    primaries := make([]string, 0, len(cloneGroups))
    for primary := range cloneGroups {
        primaries = append(primaries, primary)
    }
    cloneMutex.Unlock()

    serverMutex.Lock()
    var orphaned []string
    for _, primary := range primaries {
        if _, ok := serverList[primary]; !ok {
            orphaned = append(orphaned, primary)
        }
    }
    serverMutex.Unlock()

    releaseOrphanedGroups(orphaned)
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

// PortPaddingAddress is one address belonging to a PortPaddingIP cluster --
// either the group's surviving primary, or one of its aliases.
type PortPaddingAddress struct {
    Address   string `json:"address"`
    IsPrimary bool   `json:"is_primary"`
    Hostname  string `json:"hostname"` // live (serverList) for the primary; a one-time fold-in snapshot for an alias
    // FirstPaired/LastChecked/CheckCount surface clone_recheck.go's ongoing
    // re-verification of this specific pairing -- zero-valued for the
    // primary itself, which isn't "paired" to anything, it's the anchor.
    FirstPaired time.Time `json:"first_paired,omitempty"`
    LastChecked time.Time `json:"last_checked,omitempty"`
    CheckCount  int       `json:"check_count,omitempty"`
}

// PortPaddingIP is one IP with 2+ of a single clone group's own addresses on
// it -- primary and/or aliases, checked against each other, not just
// aliases against the primary (see PortPaddingView's doc comment for why
// that distinction matters).
type PortPaddingIP struct {
    IP        string                `json:"ip"`
    Addresses []PortPaddingAddress  `json:"addresses"`
}

// PortPaddingView is one detected clone group ("cloned server") that has at
// least one IP with 2+ of its own addresses on it -- i.e. broadcasting
// itself from several ports on some IP to inflate its presence on the list.
// One row per clone group, matching the one card it's shown as on the main
// list -- deliberately NOT merged across separate clone groups just because
// they happen to share an IP (tried that 2026-08-16, reverted: "grouping by
// ip overall makes no sense... should be per cloned server").
//
// What *did* need fixing, kept from that attempt: check every address in
// the group (primary and aliases alike) against every other, not just
// aliases against whichever address mergeGroup happened to pick as primary
// (picked by earliest FirstSeen, unrelated to which IP has the most
// members). Found 2026-08-16 against production data:
// 172.104.253.108:32026's clone group had 79 total aliases -- only 21
// shared the primary's own IP, but 18 shared 172.233.19.61, 26 shared
// 50.116.39.92, and 14 shared 66.42.93.111 amongst themselves, each
// obviously padding for that IP and never checked against each other
// before. Still one row for this one clone group, just broken down by every
// IP its own membership actually clusters on.
type PortPaddingView struct {
    Primary   string          `json:"primary"`
    Hostname  string          `json:"hostname"`
    Online    bool            `json:"online"`
    PortCount int             `json:"port_count"` // total addresses in this group: 1 (primary) + len(AKA)
    IPs       []PortPaddingIP `json:"ips"`         // only IPs with 2+ of this group's own addresses, biggest first
    Reason    string          `json:"reason"`
    Detected  time.Time       `json:"detected"`
}

// GetPortPaddingGroups returns every clone group with at least one IP
// holding 2+ of its own addresses (see PortPaddingView), most ports first.
// CloneGroup.Reason and Detected are already recorded by mergeGroup but
// were never exposed anywhere until now.
func GetPortPaddingGroups() []PortPaddingView {
    cloneMutex.Lock()
    defer cloneMutex.Unlock()

    serverMutex.Lock()
    defer serverMutex.Unlock()

    var out []PortPaddingView
    for _, g := range cloneGroups {
        var primaryHostname string
        var primaryOnline bool
        if entry, ok := serverList[g.Primary]; ok {
            primaryHostname = entry.Hostname
            primaryOnline = entry.Online
        }

        byIP := make(map[string][]PortPaddingAddress)
        primaryIP := ipOf(g.Primary)
        byIP[primaryIP] = append(byIP[primaryIP], PortPaddingAddress{Address: g.Primary, IsPrimary: true, Hostname: primaryHostname})
        for _, a := range g.AKA {
            ip := ipOf(a.Address)
            byIP[ip] = append(byIP[ip], PortPaddingAddress{
                Address: a.Address, Hostname: a.Hostname,
                FirstPaired: a.FirstPaired, LastChecked: a.LastChecked, CheckCount: a.CheckCount,
            })
        }

        var ips []PortPaddingIP
        for ip, addrs := range byIP {
            if len(addrs) < 2 {
                continue // a lone address on this IP isn't padding
            }
            sort.Slice(addrs, func(i, j int) bool { return addrs[i].Address < addrs[j].Address })
            ips = append(ips, PortPaddingIP{IP: ip, Addresses: addrs})
        }
        if len(ips) == 0 {
            continue // nothing in this group clusters on any one IP
        }
        sort.Slice(ips, func(i, j int) bool {
            if len(ips[i].Addresses) != len(ips[j].Addresses) {
                return len(ips[i].Addresses) > len(ips[j].Addresses)
            }
            return ips[i].IP < ips[j].IP
        })

        out = append(out, PortPaddingView{
            Primary:   g.Primary,
            Hostname:  primaryHostname,
            Online:    primaryOnline,
            PortCount: 1 + len(g.AKA),
            IPs:       ips,
            Reason:    g.Reason,
            Detected:  g.Detected,
        })
    }

    sort.Slice(out, func(i, j int) bool {
        if out[i].PortCount != out[j].PortCount {
            return out[i].PortCount > out[j].PortCount
        }
        return out[i].Primary < out[j].Primary
    })
    return out
}
