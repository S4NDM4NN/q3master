package main

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

type ServerStatus struct {
	Address     string    `json:"address"`
	Hostname    string    `json:"hostname"`
	Map         string    `json:"map"`
	Mod         string    `json:"mod"`
	GameType    string    `json:"gametype"`
	Version     string    `json:"version"`
	PB          string    `json:"pb"`
	PlayerCount int       `json:"player_count"`
	MaxPlayers  int       `json:"max_players"`
	Players     []string  `json:"players"`
	LastSeen    time.Time `json:"last_seen"`
}

var (
	statusCache  = make(map[string]*ServerStatus)
	statusMutex  sync.Mutex
	pollInterval = 30 * time.Second
)

func startServerPoller() {
	for {
		serverMutex.Lock()
		servers := make([]*GameServer, 0, len(serverList))
		for k, s := range serverList {
			fmt.Printf("Preparing to poll: %s (%s)\n", k, s.Addr)
			servers = append(servers, s)
		}
		serverMutex.Unlock()

		var wg sync.WaitGroup
		for _, srv := range servers {
			wg.Add(1)
			go func(s *GameServer) {
				defer wg.Done()
				pollServerStatus(s)
			}(srv)
		}
		wg.Wait()

		time.Sleep(pollInterval)
	}
}

func pollServerStatus(server *GameServer) {
	addr := server.Addr

	fmt.Printf("Polling server %s\n", addr.String())

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()

	_, err = conn.Write([]byte("\xff\xff\xff\xffgetstatus\n"))
	if err != nil {
		return
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 4096)
	n, _, err := conn.ReadFromUDP(buffer)
	fmt.Printf("Got raw status from %s: %d bytes\n", addr.String(), n)

	if err != nil {
		return
	}

	lines := strings.Split(string(buffer[:n]), "\n")
	if len(lines) < 2 {
		return
	}

	status := &ServerStatus{
		Address:  addr.String(),
		LastSeen: time.Now(),
	}

	// Parse key/value pairs
	keyValues := strings.Split(strings.TrimPrefix(lines[1], "\\"), "\\")
	for i := 0; i < len(keyValues)-1; i += 2 {
		k, v := keyValues[i], keyValues[i+1]
		switch k {
		case "sv_hostname":
			status.Hostname = v
		case "mapname":
			status.Map = v
		case "gamename":
			status.Mod = v
		case "g_gametype":
			status.GameType = v
		case "version":
			status.Version = v
		case "sv_punkbuster":
			status.PB = v
		case "sv_maxclients":
			status.MaxPlayers = parseInt(v)
		}
	}

	// Parse player list
	status.Players = []string{}
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\"", 3)
		if len(parts) >= 2 {
			status.Players = append(status.Players, parts[1])
		}
	}
	status.PlayerCount = len(status.Players)

	fmt.Printf("Parsed: %s | %s (%d/%d players)\n",
		status.Hostname, status.Map, status.PlayerCount, status.MaxPlayers)

	statusMutex.Lock()
	fmt.Printf("Storing status in cache under key: %s\n", addr.String())
	statusCache[addr.String()] = status
	statusMutex.Unlock()
}

func fetchServersFromUpstreamMaster() {
	masterAddr := "wolfmaster.idsoftware.com:27950"
	conn, err := net.Dial("udp", masterAddr)
	if err != nil {
		fmt.Printf("Failed to connect to upstream master: %v\n", err)
		return
	}
	defer conn.Close()

	request := []byte("\xff\xff\xff\xffgetservers 60\n")
	_, err = conn.Write(request)
	if err != nil {
		fmt.Printf("Failed to write to upstream master: %v\n", err)
		return
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 1400)
	n, err := conn.Read(buffer)
	if err != nil {
		fmt.Printf("Failed to read response from upstream: %v\n", err)
		return
	}

	fmt.Printf("Got %d bytes from upstream master\n", n)

	// Skip header: \xff\xff\xff\xffgetserversResponse\n
	data := buffer
	header := []byte("\xff\xff\xff\xffgetserversResponse\n")
	if bytes.HasPrefix(data, header) {
		data = data[len(header):]
	}

	// Parse 6-byte chunks (4 IP bytes, 2 port bytes)
	for i := 0; i+6 <= len(data); i += 6 {
		ip := net.IPv4(data[i], data[i+1], data[i+2], data[i+3])
		port := int(data[i+4])<<8 | int(data[i+5])
		addr := fmt.Sprintf("%s:%d", ip.String(), port)

		// Skip invalid addresses
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
			continue
		}

		serverMutex.Lock()
		if _, exists := serverList[addr]; !exists {
			udpAddr, err := net.ResolveUDPAddr("udp", addr)
			if err == nil {
				serverList[addr] = &GameServer{
					Addr:     udpAddr,
					LastSeen: time.Now(),
				}
				fmt.Printf("Discovered new server from upstream: %s\n", addr)
			}
		}
		serverMutex.Unlock()
	}
}

func startUpstreamDiscovery() {
	go func() {
		for {
			fetchServersFromUpstreamMaster()
			time.Sleep(1 * time.Minute)
		}
	}()
}
