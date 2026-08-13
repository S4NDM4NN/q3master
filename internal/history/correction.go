package history

import (
    "context"
    "sort"
    "strconv"
    "time"
)

// rawMatchTolerance bounds how far a network raw sample's timestamp may be
// from an alias's own nearest raw sample for that alias to be considered
// "present" at that point -- network sampling runs once a minute, per-
// server polling roughly every 15s independently, so their timestamps
// never align exactly.
const rawMatchTolerance = 90 * time.Second

// correctForAliases subtracts each alias's own historical contribution from
// already-computed network-total points, for the subset of aliases whose
// captured protocol matches the requested bucket. This corrects charts
// that were recorded before a clone group was known to be a duplicate,
// without ever rewriting the underlying stored samples -- a query-time
// adjustment, reapplied fresh on every request as more clones are found,
// rather than a one-time destructive backfill.
//
// It's an approximation, not an audited reconstruction: hourly/daily
// points are unweighted averages, so subtracting an alias's own average
// over roughly the same bucket approximates removing its contribution
// without being mathematically exact, and "was this alias online" for a
// bucket is approximated as "does it have any samples in that bucket"
// rather than tracking exact online fractions. Good enough for a chart;
// results are clamped at 0 in case of any imprecision.
func correctForAliases(ctx context.Context, points []NetworkPoint, protocol string, aliases map[string]int, since time.Time) []NetworkPoint {
    if len(aliases) == 0 || len(points) == 0 {
        return points
    }

    now := time.Now().UTC()
    rawCutoff := now.Add(-RawRetention)
    hourlyCutoff := now.Add(-HourlyRetention)

    playerDelta := make(map[time.Time]float64)
    onlineDelta := make(map[time.Time]float64)
    rawByAlias := make(map[string][]sampleDoc)

    for addr, aliasProto := range aliases {
        if protocol != "all" && protocol != "" && strconv.Itoa(aliasProto) != protocol {
            continue
        }

        if since.Before(hourlyCutoff) {
            if docs, err := queryDaily(ctx, addr, since, hourlyCutoff); err == nil {
                for _, d := range docs {
                    playerDelta[d.DayTs] += d.AvgPlayers
                    if d.SampleCount > 0 {
                        onlineDelta[d.DayTs]++
                    }
                }
            }
        }

        if since.Before(rawCutoff) {
            from := hourlyCutoff
            if since.After(hourlyCutoff) {
                from = since
            }
            if docs, err := queryHourly(ctx, addr, from, rawCutoff); err == nil {
                for _, d := range docs {
                    playerDelta[d.HourTs] += d.AvgPlayers
                    if d.SampleCount > 0 {
                        onlineDelta[d.HourTs]++
                    }
                }
            }
        }

        from := rawCutoff
        if since.After(rawCutoff) {
            from = since
        }
        if docs, err := queryRaw(ctx, addr, from, now); err == nil && len(docs) > 0 {
            sort.Slice(docs, func(i, j int) bool { return docs[i].Ts.Before(docs[j].Ts) })
            rawByAlias[addr] = docs
        }
    }

    corrected := make([]NetworkPoint, len(points))
    for i, p := range points {
        cp := p
        if d, ok := playerDelta[p.Ts]; ok {
            cp.TotalPlayers -= d
        }
        if d, ok := onlineDelta[p.Ts]; ok {
            cp.OnlineServerCount -= d
        }
        for _, docs := range rawByAlias {
            if d, ok := nearestSample(docs, p.Ts, rawMatchTolerance); ok {
                cp.TotalPlayers -= float64(d.PlayerCount)
                cp.OnlineServerCount--
            }
        }
        if cp.TotalPlayers < 0 {
            cp.TotalPlayers = 0
        }
        if cp.OnlineServerCount < 0 {
            cp.OnlineServerCount = 0
        }
        corrected[i] = cp
    }
    return corrected
}

// nearestSample returns the doc in a Ts-sorted slice closest to target, if
// within tolerance.
func nearestSample(docs []sampleDoc, target time.Time, tolerance time.Duration) (sampleDoc, bool) {
    idx := sort.Search(len(docs), func(i int) bool { return !docs[i].Ts.Before(target) })

    best := -1
    bestDiff := tolerance + 1
    for _, cand := range []int{idx - 1, idx} {
        if cand < 0 || cand >= len(docs) {
            continue
        }
        diff := docs[cand].Ts.Sub(target)
        if diff < 0 {
            diff = -diff
        }
        if diff <= tolerance && diff < bestDiff {
            best = cand
            bestDiff = diff
        }
    }
    if best == -1 {
        return sampleDoc{}, false
    }
    return docs[best], true
}
