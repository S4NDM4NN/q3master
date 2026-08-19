package servers

import (
    "sort"
    "time"
)

// pollHealthOverdueThreshold flags an online server as "overdue" for repoll
// once this long has passed since its last poll attempt. StartPolling's
// passive sweep re-queues an online server as soon as its LastSeen turns 2
// minutes old, so under normal poll-worker throughput LastAttempt should
// refresh on roughly that cadence; this threshold sits comfortably below
// the 5-minute online->offline cutoff janitor.go applies to LastSeen, so a
// rising overdue count is an early warning that poll-worker/per-IP-pacing
// throughput is falling behind *before* it starts incorrectly flipping
// still-up servers offline -- see main.go's note on why poll workers were
// raised from 8 to 100 for exactly this failure mode.
const pollHealthOverdueThreshold = 3 * time.Minute

// ProtocolPollHealth is one protocol's repoll health, part of PollHealth.
type ProtocolPollHealth struct {
    Protocol      int `json:"protocol"`
    Online        int `json:"online"`
    Overdue       int `json:"overdue"`
    MaxGapSeconds int `json:"max_gap_seconds"`
}

// PollHealth summarizes how far behind the poll-worker pool is on
// repolling currently-online servers, broken down by protocol -- see
// pollHealthOverdueThreshold.
type PollHealth struct {
    GeneratedAt             time.Time             `json:"generated_at"`
    OverdueThresholdSeconds int                   `json:"overdue_threshold_seconds"`
    TotalOnline             int                   `json:"total_online"`
    TotalOverdue            int                   `json:"total_overdue"`
    ByProtocol              []ProtocolPollHealth  `json:"by_protocol"`
}

// GetPollHealth computes PollHealth from the current serverList snapshot.
func GetPollHealth() PollHealth {
    now := time.Now()

    type acc struct {
        online, overdue int
        maxGap          time.Duration
    }
    byProto := make(map[int]*acc)

    serverMutex.Lock()
    for _, s := range serverList {
        if !s.Online {
            continue
        }
        a, ok := byProto[s.Protocol]
        if !ok {
            a = &acc{}
            byProto[s.Protocol] = a
        }
        a.online++
        if s.LastAttempt.IsZero() {
            continue
        }
        gap := now.Sub(s.LastAttempt)
        if gap > a.maxGap {
            a.maxGap = gap
        }
        if gap > pollHealthOverdueThreshold {
            a.overdue++
        }
    }
    serverMutex.Unlock()

    protos := make([]int, 0, len(byProto))
    for p := range byProto {
        protos = append(protos, p)
    }
    sort.Ints(protos)

    out := PollHealth{
        GeneratedAt:             now,
        OverdueThresholdSeconds: int(pollHealthOverdueThreshold.Seconds()),
    }
    for _, p := range protos {
        a := byProto[p]
        out.ByProtocol = append(out.ByProtocol, ProtocolPollHealth{
            Protocol:      p,
            Online:        a.online,
            Overdue:       a.overdue,
            MaxGapSeconds: int(a.maxGap.Seconds()),
        })
        out.TotalOnline += a.online
        out.TotalOverdue += a.overdue
    }
    return out
}
