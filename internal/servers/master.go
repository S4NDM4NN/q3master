package servers

import (
    "bytes"
    "fmt"
    "net"
    "time"
)

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
                return
            }

            for {
                conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
                buffer := make([]byte, 1400)
                n, err := conn.Read(buffer)
                if err != nil {
                    break
                }

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
                        go pollServer(serverList[addr])
                    }
                    serverMutex.Unlock()
                }
            }
        }()
    }
}

