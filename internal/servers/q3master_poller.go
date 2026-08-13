package servers

import (
    "bytes"
    "fmt"
    "net"
    "sync"
    "time"
)

// MasterStatus reports whether the real id Software master is reachable.
type MasterStatus struct {
    Up          bool      `json:"up"`
    LastChecked time.Time `json:"last_checked"`
    LastSuccess time.Time `json:"last_success"`
}

// MasterHostStatus is the reachability of one entry in knownMasters, for
// the multi-master status page (as opposed to MasterStatus, which is
// specifically the official master alone, used by the main page's status
// line).
type MasterHostStatus struct {
    Host        string    `json:"host"`
    Label       string    `json:"label"`
    Up          bool      `json:"up"`
    LastChecked time.Time `json:"last_checked"`
    LastSuccess time.Time `json:"last_success"`
}

var (
    masterStatus      MasterStatus
    masterStatusMutex sync.Mutex

    allMasterStatus      = make(map[string]*MasterHostStatus, len(knownMasters))
    allMasterStatusMutex sync.Mutex
)

// GetMasterStatus returns the last known reachability of the real master.
func GetMasterStatus() MasterStatus {
    masterStatusMutex.Lock()
    defer masterStatusMutex.Unlock()
    return masterStatus
}

func setMasterStatus(up bool) {
    masterStatusMutex.Lock()
    defer masterStatusMutex.Unlock()
    masterStatus.LastChecked = time.Now()
    masterStatus.Up = up
    if up {
        masterStatus.LastSuccess = masterStatus.LastChecked
    }
}

// GetAllMasterStatuses returns the last known reachability of every master
// in knownMasters, in that order.
func GetAllMasterStatuses() []MasterHostStatus {
    allMasterStatusMutex.Lock()
    defer allMasterStatusMutex.Unlock()
    out := make([]MasterHostStatus, 0, len(knownMasters))
    for _, m := range knownMasters {
        if s, ok := allMasterStatus[m.Host]; ok {
            out = append(out, *s)
        } else {
            out = append(out, MasterHostStatus{Host: m.Host, Label: m.Label})
        }
    }
    return out
}

func setAllMasterStatus(host, label string, up bool) {
    allMasterStatusMutex.Lock()
    defer allMasterStatusMutex.Unlock()
    s, ok := allMasterStatus[host]
    if !ok {
        s = &MasterHostStatus{Host: host, Label: label}
        allMasterStatus[host] = s
    }
    s.LastChecked = time.Now()
    s.Up = up
    if up {
        s.LastSuccess = s.LastChecked
    }
}

// StartDiscovery periodically refreshes server addresses from the master
// list. record, if non-nil, is called after each discovery attempt once
// per known master with its host and whether it was reachable (for history
// tracking).
func StartDiscovery(interval time.Duration, record func(host string, up bool)) {
    go func() {
        for {
            refreshFromMaster(record)
            time.Sleep(interval)
        }
    }()
}

// refreshFromMaster polls every known master (knownMasters) for each
// protocol to find server addresses, merging results -- serverList is
// keyed by address, so duplicates found via more than one master collapse
// naturally. It also records each master's individual reachability (for
// the multi-master status page) and, specifically for the official id
// Software master, the single-master MasterStatus the main page's status
// line has always used.
func refreshFromMaster(record func(host string, up bool)) {
    for _, m := range knownMasters {
        hostUp := false
        for _, proto := range protocols {
            if queryMaster(m.Host, proto) {
                hostUp = true
            }
        }
        setAllMasterStatus(m.Host, m.Label, hostUp)
        if m.Host == masterHost {
            setMasterStatus(hostUp)
        }
        if record != nil {
            record(m.Host, hostUp)
        }
    }
}

// queryMaster sends a getservers request to one master for one protocol,
// adding any newly-discovered addresses to serverList, and reports whether
// the master responded at all.
func queryMaster(host, proto string) bool {
    conn, err := net.Dial("udp", host)
    if err != nil {
        fmt.Printf("Error connecting to master %s: %v\n", host, err)
        return false
    }
    defer conn.Close()

    _, err = conn.Write([]byte(fmt.Sprintf("\xff\xff\xff\xffgetservers %s full empty", proto)))
    if err != nil {
        fmt.Printf("Error sending getservers to master %s (protocol %s): %v\n", host, proto, err)
        return false
    }

    gotResponse := false
    for {
        // Allow more time for multi-packet responses from master
        conn.SetReadDeadline(time.Now().Add(2 * time.Second))
        buffer := make([]byte, 1400)
        n, err := conn.Read(buffer)
        if err != nil {
            if !gotResponse {
                fmt.Printf("No response from master %s (protocol %s): %v\n", host, proto, err)
            }
            break
        }
        gotResponse = true

        data := buffer[:n]
        if bytes.HasPrefix(data, []byte("\xff\xff\xff\xffgetserversResponse\n")) {
            data = data[len("\xff\xff\xff\xffgetserversResponse\n"):]
        }
        if len(data) > 0 && data[len(data)-1] == 0x00 {
            data = data[:len(data)-1]
        }

        for i := 0; i+6 <= len(data); {
            if data[i] == '\\' {
                i++
                continue
            }

            ip := net.IPv4(data[i], data[i+1], data[i+2], data[i+3])
            port := int(data[i+4])<<8 | int(data[i+5])
            i += 6

            if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
                continue
            }

            addr := fmt.Sprintf("%s:%d", ip.String(), port)

            serverMutex.Lock()
            if _, exists := serverList[addr]; !exists {
                serverList[addr] = &ServerEntry{
                    Address:     addr,
                    Protocol:    parseInt(proto),
                    State:       StateNew,
                    FirstSeen:   time.Now(),
                    LastAttempt: time.Time{},
                }
                // Queue a poll instead of spawning unbounded goroutines
                EnqueuePoll(addr)
            }
            serverMutex.Unlock()
        }
    }
    return gotResponse
}
