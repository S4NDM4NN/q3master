package servers

import (
    "fmt"
    "sort"
    "time"
)

// recheckBatchSize caps how many aliases get rechecked per tick, so a large
// backlog of newly-due aliases (e.g. right after this feature first ships,
// when every existing pairing looks overdue at once -- see dueForRecheck)
// can't burst a lot of getstatus traffic in one go. Same spirit as
// minPollIntervalPerIP's per-IP pacing in the normal poller, just budgeted
// per recheck cycle instead of per IP.
const recheckBatchSize = 20

// recheckTickInterval is how often the recheck loop wakes up to look for
// due aliases. Independent of any single alias's own backoff interval (see
// recheckIntervalLadder) -- this just needs to be frequent enough that the
// shortest rung (15 min) is actually honored reasonably promptly, and that
// a large backlog (bounded by recheckBatchSize per tick) clears in a
// reasonable number of ticks.
const recheckTickInterval = 5 * time.Minute

// recheckIntervalLadder is how long to wait before re-verifying a pairing
// again, indexed by AKAEntry.CheckCount -- how many *consecutive* rechecks
// have already confirmed it still matches its primary. Starts fast (catch a
// bad pairing quickly, like the tier-1 false positive found 2026-08-16 --
// see realRosterMinSize) and relaxes as the pairing proves stable, capping
// at once a day once there's been enough consecutive churn-free history.
var recheckIntervalLadder = []time.Duration{
    15 * time.Minute,
    1 * time.Hour,
    4 * time.Hour,
    24 * time.Hour,
}

// recheckInterval returns how long to wait before the next recheck, given
// checkCount consecutive confirmations so far. Indices past the ladder's
// end all get the last (longest) rung -- there's no reason to keep growing
// the interval indefinitely once a pairing's this mature.
func recheckInterval(checkCount int) time.Duration {
    if checkCount >= len(recheckIntervalLadder) {
        return recheckIntervalLadder[len(recheckIntervalLadder)-1]
    }
    return recheckIntervalLadder[checkCount]
}

// dueForRecheck reports whether e is due for its next recheck: its last
// recheck (or, if it's never been rechecked, when it was first paired) plus
// however long its current rung on recheckIntervalLadder calls for.
func dueForRecheck(e AKAEntry, now time.Time) bool {
    last := e.LastChecked
    if last.IsZero() {
        last = e.FirstPaired
    }
    return now.Sub(last) >= recheckInterval(e.CheckCount)
}

// StartAliasRecheck periodically re-verifies a bounded batch of due aliases
// still actually match their primary's current content, unpairing (see
// unpairAlias) any that no longer do -- restoring them as independent,
// normally-pollable entries instead of leaving a stale or outright
// incorrect merge in place forever. Without this, a bad pairing is
// permanent: applyCloneGroups deletes an alias's ServerEntry outright and
// isKnownAlias blocks it from ever re-entering via discovery or heartbeat.
func StartAliasRecheck() {
    go func() {
        for {
            time.Sleep(recheckTickInterval)
            recheckDueAliases()
        }
    }()
}

// recheckDueAliases selects up to recheckBatchSize due aliases across every
// clone group (oldest-due first, so a large backlog drains in FIFO order
// across ticks rather than starving whichever group happens to sort last),
// and re-verifies each in turn.
func recheckDueAliases() {
    now := time.Now()

    type due struct {
        primary string
        alias   AKAEntry
    }
    var candidates []due

    cloneMutex.Lock()
    for primary, g := range cloneGroups {
        for _, a := range g.AKA {
            if dueForRecheck(a, now) {
                candidates = append(candidates, due{primary: primary, alias: a})
            }
        }
    }
    cloneMutex.Unlock()

    if len(candidates) == 0 {
        return
    }

    sort.Slice(candidates, func(i, j int) bool {
        li, lj := candidates[i].alias.LastChecked, candidates[j].alias.LastChecked
        if li.IsZero() {
            li = candidates[i].alias.FirstPaired
        }
        if lj.IsZero() {
            lj = candidates[j].alias.FirstPaired
        }
        return li.Before(lj)
    })
    if len(candidates) > recheckBatchSize {
        candidates = candidates[:recheckBatchSize]
    }

    for _, c := range candidates {
        recheckOne(c.primary, c.alias.Address)
    }
}

// recheckOne re-polls one alias directly with a fresh getstatus (it has no
// ServerEntry of its own -- ordinary polling never sees it while paired)
// and compares the result against its primary's current serverList data.
// Confirms by advancing the alias's recheck streak; contradicts by
// unpairing it.
func recheckOne(primaryAddr, aliasAddr string) {
    primary, ok := GetServer(primaryAddr)
    if !ok || !primary.Online {
        // Nothing current to compare against -- leave the alias's schedule
        // as-is (still due, so it's picked up again next tick) rather than
        // guess either way. By then the primary may be back online.
        return
    }

    fresh, ok := queryGetStatus(aliasAddr)
    if !ok {
        // Alias didn't answer. Not itself evidence of a bad pairing --
        // aliases on a busy padding relay routinely have long dead
        // stretches (see the ghost-port findings recorded elsewhere in this
        // project) -- so this doesn't unpair or advance the streak, just
        // retries on the same schedule.
        bumpAliasCheck(primaryAddr, aliasAddr, false)
        return
    }

    if stillMatches(primary, fresh) {
        bumpAliasCheck(primaryAddr, aliasAddr, true)
        return
    }

    unpairAlias(primaryAddr, aliasAddr, fresh)
}

// stillMatches applies whichever of detectClones' three tiers the alias's
// *current* content qualifies for, rather than whichever tier applied back
// when it was first paired -- a server's own state routinely moves between
// having real players, bots-only, and empty over time, and the recheck
// should judge it by what it's actually reporting now.
func stillMatches(primary, fresh *ServerEntry) bool {
    switch {
    case len(fresh.Players) > 0 && len(primary.Players) > 0:
        if primary.Map != fresh.Map {
            return false
        }
        a := rosterSet(primary.Players, primary.Bots)
        b := rosterSet(fresh.Players, fresh.Bots)
        diff := rosterSymmetricDiff(a, b)
        return diff <= realRosterMaxDrift && len(a) >= realRosterMinSize && len(b) >= realRosterMinSize
    case len(fresh.Bots) > 0 && len(primary.Bots) > 0 && len(primary.Players) == 0 && len(fresh.Players) == 0:
        return primary.Hostname == fresh.Hostname && primary.Map == fresh.Map && sameBotRoster(primary.Bots, fresh.Bots)
    case fresh.HasMatchTime && primary.HasMatchTime && len(fresh.Players) == 0 && len(fresh.Bots) == 0:
        // The match-time tier's original bar (clock consistency between two
        // *simultaneous* polls, see matchTimeTolerance) needs a second poll
        // of the primary at a matching instant to check the same way
        // detectClones does -- not available here without polling the
        // primary again too. Hostname+map agreement is a weaker bar, but
        // the pairing already cleared the stronger one once at merge time;
        // this recheck exists to catch it drifting away, not to re-litigate
        // the original match from scratch.
        return primary.Hostname == fresh.Hostname && primary.Map == fresh.Map
    default:
        // The two sides no longer even have comparable content (e.g. one
        // has real players now and the other doesn't) -- not a match.
        return false
    }
}

// rosterSet builds the same {player}∪{bot} name-presence set detectClones
// uses for tier 1's roster comparison.
func rosterSet(players, bots []string) map[string]bool {
    m := make(map[string]bool, len(players)+len(bots))
    for _, n := range players {
        m[n] = true
    }
    for _, n := range bots {
        m[n] = true
    }
    return m
}

// sameBotRoster reports whether a and b contain the same names, order
// ignored -- mirrors tier 2's exact (sorted-join) bot-roster comparison in
// detectClones without needing the two sides pre-sorted-and-joined the same
// way that code path constructs its map key.
func sameBotRoster(a, b []string) bool {
    if len(a) != len(b) {
        return false
    }
    sa := append([]string(nil), a...)
    sb := append([]string(nil), b...)
    sort.Strings(sa)
    sort.Strings(sb)
    for i := range sa {
        if sa[i] != sb[i] {
            return false
        }
    }
    return true
}

// bumpAliasCheck records that alias was rechecked: LastChecked always
// advances, but CheckCount -- which is what actually slows the recheck
// interval down (see recheckIntervalLadder) -- only advances on a
// confirming result, so a no-response poll doesn't get treated the same as
// active reconfirmation.
func bumpAliasCheck(primaryAddr, aliasAddr string, confirmed bool) {
    cloneMutex.Lock()
    defer cloneMutex.Unlock()

    g, ok := cloneGroups[primaryAddr]
    if !ok {
        return
    }
    for i := range g.AKA {
        if g.AKA[i].Address != aliasAddr {
            continue
        }
        g.AKA[i].LastChecked = time.Now()
        if confirmed {
            g.AKA[i].CheckCount++
        }
        return
    }
}

// unpairAlias removes alias from primary's clone group -- a recheck found
// it no longer matches what originally justified the pairing -- and
// restores it as an ordinary, independently pollable ServerEntry, exactly
// as if it were being discovered fresh. fresh is the just-polled data that
// contradicted the pairing, seeded directly onto the restored entry so it
// doesn't have to wait for its own first ordinary poll to show real
// content.
func unpairAlias(primaryAddr, aliasAddr string, fresh *ServerEntry) {
    cloneMutex.Lock()
    g, ok := cloneGroups[primaryAddr]
    if !ok {
        cloneMutex.Unlock()
        return
    }
    kept := g.AKA[:0]
    for _, a := range g.AKA {
        if a.Address != aliasAddr {
            kept = append(kept, a)
        }
    }
    g.AKA = kept
    delete(aliasToPrimary, aliasAddr)
    cloneMutex.Unlock()

    fresh.Address = aliasAddr
    fresh.State = StateOnline
    fresh.Online = true
    fresh.FirstSeen = time.Now()
    fresh.LastGoodPoll = time.Now()

    serverMutex.Lock()
    serverList[aliasAddr] = fresh
    serverMutex.Unlock()

    fmt.Printf("unpaired %s from clone group %s: no longer matches its primary on recheck\n", aliasAddr, primaryAddr)
}
