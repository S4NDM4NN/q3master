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

var (
    masterStatus      MasterStatus
    masterStatusMutex sync.Mutex
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

// StartDiscovery periodically refreshes server addresses from the master list.
func StartDiscovery(interval time.Duration) {
    go func() {
        for {
            refreshFromMaster()
            time.Sleep(interval)
        }
    }()
}

func refreshFromMaster() {
    anySuccess := false
    for _, proto := range protocols {
        conn, err := net.Dial("udp", masterHost)
        if err != nil {
            fmt.Printf("Error connecting to master: %v\n", err)
            continue
        }
        // ensure connection closes for each protocol iteration
        func() {
            defer conn.Close()

            _, err = conn.Write([]byte(fmt.Sprintf("\xff\xff\xff\xffgetservers %s full empty", proto)))
            if err != nil {
                fmt.Printf("Error sending getservers to master %s (protocol %s): %v\n", masterHost, proto, err)
                return
            }

            gotResponse := false
            for {
                // Allow more time for multi-packet responses from master
                conn.SetReadDeadline(time.Now().Add(2 * time.Second))
                buffer := make([]byte, 1400)
                n, err := conn.Read(buffer)
                if err != nil {
                    if !gotResponse {
                        fmt.Printf("No response from master %s (protocol %s): %v\n", masterHost, proto, err)
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
            if gotResponse {
                anySuccess = true
            }
        }()
    }
    setMasterStatus(anySuccess)
}
