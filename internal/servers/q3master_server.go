package servers

import (
    "bytes"
    "fmt"
    "net"
    "strconv"
    "strings"
    "sync"
    "time"
)

// StartMasterUDP starts a UDP listener that:
// - responds to getservers queries with our in-memory list
// - accepts heartbeat/shutdown messages from game servers
func StartMasterUDP(addr string) {
    go func() {
        udpAddr, err := net.ResolveUDPAddr("udp", addr)
        if err != nil {
            fmt.Printf("master udp resolve error: %v\n", err)
            return
        }
        conn, err := net.ListenUDP("udp", udpAddr)
        if err != nil {
            fmt.Printf("master udp listen error: %v\n", err)
            return
        }
        fmt.Printf("Master UDP listening on %s\n", conn.LocalAddr())
        // Best-effort enlarge buffers to handle bursts
        _ = conn.SetReadBuffer(1 << 20)
        _ = conn.SetWriteBuffer(1 << 20)

        buf := make([]byte, 2048)
        for {
            n, raddr, err := conn.ReadFromUDP(buf)
            if err != nil {
                continue
            }
            data := buf[:n]

            // Strip quake3 four 0xFF prefix if present
            if len(data) >= 4 && bytes.Equal(data[:4], []byte{0xff, 0xff, 0xff, 0xff}) {
                data = data[4:]
            }
            line := strings.TrimRight(string(data), "\x00\n\r ")
            lc := strings.ToLower(line)

            switch {
            case strings.HasPrefix(lc, "getservers"):
                if allowRequest(raddr.IP.String(), kindGetServers) {
                    handleGetServers(conn, raddr, line)
                }
            case strings.HasPrefix(lc, "heartbeat"):
                if allowRequest(raddr.IP.String(), kindHeartbeat) {
                    handleHeartbeat(raddr, line)
                }
            case strings.HasPrefix(lc, "shutdown"):
                if allowRequest(raddr.IP.String(), kindShutdown) {
                    handleShutdown(raddr)
                }
            default:
                // ignore
            }
        }
    }()
}

// --- Simple token-bucket rate limiting ---

type reqKind int

const (
    kindGetServers reqKind = iota
    kindHeartbeat
    kindShutdown
)

type bucket struct {
    tokens     float64
    lastRefill time.Time
    rate       float64 // tokens per second
    burst      float64 // max tokens
}

var (
    // dedicated rate-limit mutex; do not alias serverMutex
    rlMutex      sync.Mutex
    ipBuckets    = make(map[string]*bucket)
    globalBucket = &bucket{tokens: 50, lastRefill: time.Now(), rate: 50, burst: 100}
)

func cfgFor(kind reqKind) (rate, burst float64) {
    switch kind {
    case kindGetServers:
        return 1.5, 4 // per-IP rate for getservers
    case kindHeartbeat:
        return 2, 4
    case kindShutdown:
        return 0.5, 1
    default:
        return 1, 2
    }
}

func take(b *bucket, need float64) bool {
    now := time.Now()
    elapsed := now.Sub(b.lastRefill).Seconds()
    b.tokens = minf(b.burst, b.tokens+elapsed*b.rate)
    b.lastRefill = now
    if b.tokens >= need {
        b.tokens -= need
        return true
    }
    return false
}

func allowRequest(ip string, kind reqKind) bool {
    rlMutex.Lock()
    defer rlMutex.Unlock()
    if !take(globalBucket, 1) {
        return false
    }
    b, ok := ipBuckets[ip]
    if !ok {
        r, br := cfgFor(kind)
        b = &bucket{tokens: br, lastRefill: time.Now(), rate: r, burst: br}
        ipBuckets[ip] = b
    } else {
        r, br := cfgFor(kind)
        b.rate, b.burst = r, br
    }
    return take(b, 1)
}

func minf(a, b float64) float64 { if a < b { return a } ; return b }

func handleHeartbeat(raddr *net.UDPAddr, line string) {
    addr := net.JoinHostPort(raddr.IP.String(), strconv.Itoa(raddr.Port))
    serverMutex.Lock()
    s, ok := serverList[addr]
    if !ok {
        s = &ServerEntry{
            Address:     addr,
            Protocol:    0, // will be filled by poller
            State:       StateNew,
            FirstSeen:   time.Now(),
            LastAttempt: time.Time{},
        }
        serverList[addr] = s
        EnqueuePoll(addr)
    }
    // Heartbeats suggest the server is alive; record and queue a poll soon
    s.MissedPolls = 0
    s.LastHeartbeat = time.Now()
    s.Heartbeats++
    serverMutex.Unlock()
}

func handleShutdown(raddr *net.UDPAddr) {
    addr := net.JoinHostPort(raddr.IP.String(), strconv.Itoa(raddr.Port))
    serverMutex.Lock()
    s := serverList[addr]
    if s != nil {
        recent := false
        if !s.LastHeartbeat.IsZero() && time.Since(s.LastHeartbeat) < 5*time.Minute {
            recent = true
        }
        if !recent && !s.LastGoodPoll.IsZero() && time.Since(s.LastGoodPoll) < 5*time.Minute {
            recent = true
        }
        if recent {
            delete(serverList, addr)
        }
    }
    serverMutex.Unlock()
}

func handleGetServers(conn *net.UDPConn, raddr *net.UDPAddr, line string) {
    // Expected: "getservers <protocol> [full] [empty] ..."
    fields := strings.Fields(line)
    var protoReq int
    if len(fields) >= 2 {
        protoReq = parseInt(fields[1])
    }

    // Build response(s) with chunking to avoid oversized UDP packets
    header := []byte("\xff\xff\xff\xffgetserversResponse\n")
    // Keep chunks under ~1200 bytes for safety
    const maxChunk = 1200

    flush := func(entries [][]byte) {
        if len(entries) == 0 {
            return
        }
        var pkt []byte
        pkt = append(pkt, header...)
        for _, e := range entries {
            pkt = append(pkt, '\\')
            pkt = append(pkt, e...)
        }
        // Terminator that many clients expect
        pkt = append(pkt, '\\')
        pkt = append(pkt, []byte("EOT")...)
        pkt = append(pkt, 0x00)
        _, _ = conn.WriteToUDP(pkt, raddr)
    }

    makeEntry := func(ip net.IP, port int) []byte {
        v4 := ip.To4()
        if v4 == nil {
            return nil
        }
        return []byte{v4[0], v4[1], v4[2], v4[3], byte((port >> 8) & 0xff), byte(port & 0xff)}
    }

    // Collect entries with filtering
    serverMutex.Lock()
    entries := make([][]byte, 0, len(serverList))
    for _, s := range serverList {
        if protoReq != 0 && s.Protocol != 0 && s.Protocol != protoReq {
            continue
        }
        host, portStr, err := net.SplitHostPort(s.Address)
        if err != nil { continue }
        ip := net.ParseIP(host)
        if ip == nil || !ip.IsGlobalUnicast() { continue }
        port, err := strconv.Atoi(portStr)
        if err != nil { continue }
        e := makeEntry(ip, port)
        if e != nil {
            entries = append(entries, e)
        }
    }
    serverMutex.Unlock()

    // Chunk and send
    cur := make([][]byte, 0)
    curSize := len(header) + 5 // approx terminator
    for _, e := range entries {
        // each entry contributes 1 (backslash) + 6 bytes
        if curSize+7 > maxChunk {
            flush(cur)
            cur = cur[:0]
            curSize = len(header) + 5
        }
        cur = append(cur, e)
        curSize += 7
    }
    flush(cur)
}
