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

// autoIgnored holds IPs the clone-abuse detector below has found on its
// own, separate from the manually curated ignoredHosts list in types.go so
// the two can be reasoned about (and persisted) independently. Unlike
// ignoredHosts, this needs no code change/deploy to grow -- it's found and
// blocked live.
var (
    autoIgnored      = make(map[string]string) // ip -> reason
    autoIgnoredMutex sync.Mutex
)

// detectCloneAbuse groups all known online servers by *content* --
// hostname, map, and player+bot roster -- regardless of address, and flags
// every IP behind any group of 2+ addresses that all match. Deliberately
// not scoped to "same IP, different ports": the confirmed real case
// (155.138.197.166, added to the curated list below before this detector
// existed) was one IP cloned across 8 ports, but the same trick works just
// as well spread across several different IPs, so matching purely on
// content and blocking every IP involved in a match covers both. A
// non-empty roster is required, so two coincidentally-identical *empty*
// default-config servers don't get flagged on hostname/map alone.
func detectCloneAbuse() {
    type cloneKey struct {
        hostname, mapName, roster string
    }
    groups := make(map[cloneKey][]string) // addresses, possibly spanning several IPs

    serverMutex.Lock()
    for addr, s := range serverList {
        if !s.Online || s.Hostname == "" {
            continue
        }
        roster := make([]string, 0, len(s.Players)+len(s.Bots))
        roster = append(roster, s.Players...)
        roster = append(roster, s.Bots...)
        if len(roster) == 0 {
            continue
        }
        sort.Strings(roster)

        k := cloneKey{s.Hostname, s.Map, strings.Join(roster, "\x00")}
        groups[k] = append(groups[k], addr)
    }
    serverMutex.Unlock()

    for k, addrs := range groups {
        if len(addrs) < 2 {
            continue
        }
        sort.Strings(addrs)

        ipSet := make(map[string]bool)
        for _, a := range addrs {
            ip := a
            if idx := strings.LastIndex(a, ":"); idx != -1 {
                ip = a[:idx]
            }
            ipSet[ip] = true
        }
        ips := make([]string, 0, len(ipSet))
        for ip := range ipSet {
            ips = append(ips, ip)
        }
        sort.Strings(ips)

        reason := fmt.Sprintf(
            "Auto-detected %s: %d addresses across %d IP(s) (%s) all reporting identical hostname %q, map %q, and player/bot roster -- one server padded to look like %d.",
            time.Now().UTC().Format("2006-01-02"), len(addrs), len(ips), strings.Join(addrs, ", "), k.hostname, k.mapName, len(addrs),
        )
        for _, ip := range ips {
            addAutoIgnored(ip, reason)
        }
    }
}

// addAutoIgnored blocks ip (unless already blocked, curated or previously
// auto-detected) and purges any of its existing serverList entries.
func addAutoIgnored(ip, reason string) {
    if _, already := ignoredReasonForIP(ip); already {
        return
    }
    autoIgnoredMutex.Lock()
    autoIgnored[ip] = reason
    autoIgnoredMutex.Unlock()

    fmt.Printf("abuse detection: blocking %s: %s\n", ip, reason)

    serverMutex.Lock()
    prefix := ip + ":"
    for addr := range serverList {
        if strings.HasPrefix(addr, prefix) {
            delete(serverList, addr)
        }
    }
    serverMutex.Unlock()
}

// StartAbuseDetection periodically runs detectCloneAbuse and persists any
// newly-found blocks to persistPath, so a detected abuser stays blocked
// across restarts/redeploys instead of needing to be rediscovered from
// scratch every time the process starts.
func StartAbuseDetection(interval time.Duration, persistPath string) {
    go func() {
        for {
            time.Sleep(interval)
            detectCloneAbuse()
            if err := SaveAutoIgnored(persistPath); err != nil {
                fmt.Printf("failed to persist auto-ignored state: %v\n", err)
            }
        }
    }()
}

// SaveAutoIgnored/LoadAutoIgnored persist auto-detected blocks to their own
// file, deliberately separate from the main server state file, so loading
// one can't be broken by a format change in the other.
func SaveAutoIgnored(path string) error {
    autoIgnoredMutex.Lock()
    snapshot := make(map[string]string, len(autoIgnored))
    for ip, reason := range autoIgnored {
        snapshot[ip] = reason
    }
    autoIgnoredMutex.Unlock()

    data, err := json.MarshalIndent(snapshot, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal auto-ignored state: %w", err)
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return fmt.Errorf("ensure auto-ignored state dir: %w", err)
    }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil {
        return fmt.Errorf("write temp auto-ignored file: %w", err)
    }
    return os.Rename(tmp, path)
}

// LoadAutoIgnored loads previously-persisted auto-detected blocks. It is
// not an error for the file to be missing (nothing detected yet).
func LoadAutoIgnored(path string) error {
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return fmt.Errorf("read auto-ignored file: %w", err)
    }
    var loaded map[string]string
    if err := json.Unmarshal(data, &loaded); err != nil {
        return fmt.Errorf("unmarshal auto-ignored file: %w", err)
    }
    autoIgnoredMutex.Lock()
    for ip, reason := range loaded {
        autoIgnored[ip] = reason
    }
    autoIgnoredMutex.Unlock()
    return nil
}
