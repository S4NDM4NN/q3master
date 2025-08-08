package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ServerEntry struct {
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
	Polls       int       `json:"polls"`
	LastSeen    time.Time `json:"last_seen"`
	Online      bool      `json:"online"`
	Protocol    int       `json:"protocol"`
	Bots        []string  `json:"bots"`
	BotCount    int       `json:"bot_count"`
}

var (
	serverList  = make(map[string]*ServerEntry)
	serverMutex sync.Mutex
	protocols   = []string{"57", "60", "84"}
	masterHost  = "wolfmaster.idsoftware.com:27950"
)

func main() {
	go func() {
		for {
			refreshFromMaster()
			time.Sleep(5 * time.Minute)
		}
	}()

	go func() {
		for {
			pollServers()
			time.Sleep(15 * time.Second)
		}
	}()

	http.HandleFunc("/api/servers", withCORS(serveAPI))
	http.Handle("/", http.FileServer(http.Dir("web")))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Println("Listening on :" + port)
	http.ListenAndServe(":"+port, nil)
}

func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

func refreshFromMaster() {
	for _, proto := range protocols {
		conn, err := net.Dial("udp", masterHost)
		if err != nil {
			fmt.Printf("Error connecting to master: %v\n", err)
			continue
		}
		defer conn.Close()

		_, err = conn.Write([]byte(fmt.Sprintf("\xff\xff\xff\xffgetservers %s full empty", proto)))
		if err != nil {
			continue
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
				// Look for a 6-byte marker
				if data[i] == '\\' {
					i++ // skip separator (some servers include slashes)
					continue
				}

				ip := net.IPv4(data[i], data[i+1], data[i+2], data[i+3])
				port := int(data[i+4])<<8 | int(data[i+5])
				i += 6

				if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
					continue // skip invalid addresses
				}

				addr := fmt.Sprintf("%s:%d", ip.String(), port)

				serverMutex.Lock()
				if _, exists := serverList[addr]; !exists {
					serverList[addr] = &ServerEntry{
						Address:  addr,
						Protocol: parseInt(proto),
					}
					go pollServer(serverList[addr])
				}
				serverMutex.Unlock()
			}
		}
	}
}

func pollServers() {
	serverMutex.Lock()
	now := time.Now()
	var toPoll []*ServerEntry

	for _, s := range serverList {
		if !s.Online || now.Sub(s.LastSeen) > 2*time.Minute {
			toPoll = append(toPoll, s)
		}
	}
	serverMutex.Unlock()

	for _, s := range toPoll {
		go pollServer(s)
	}
}

func pollServer(s *ServerEntry) {
	serverMutex.Lock()
	s.Polls++
	serverMutex.Unlock()

	addr, err := net.ResolveUDPAddr("udp", s.Address)
	if err != nil {
		return
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		markOffline(s)
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = conn.Write([]byte("\xff\xff\xff\xffgetstatus\n"))
	if err != nil {
		markOffline(s)
		return
	}

	buffer := make([]byte, 4096)
	n, err := conn.Read(buffer)
	if err != nil || n == 0 {
		markOffline(s)
		return
	}

	lines := strings.Split(string(buffer[:n]), "\n")
	if len(lines) < 2 {
		markOffline(s)
		return
	}

	keyValues := strings.Split(strings.TrimPrefix(lines[1], "\\"), "\\")
	newStatus := &ServerEntry{
		LastSeen: time.Now(),
	}

	for i := 0; i < len(keyValues)-1; i += 2 {
		k, v := keyValues[i], keyValues[i+1]
		switch k {
		case "sv_hostname":
			newStatus.Hostname = v
		case "mapname":
			newStatus.Map = v
		case "gamename":
			newStatus.Mod = v
		case "g_gametype":
			newStatus.GameType = v
		case "version":
			newStatus.Version = v
		case "sv_punkbuster":
			newStatus.PB = v
		case "sv_maxclients":
			newStatus.MaxPlayers = parseInt(v)
		}
	}

	newStatus.Players = []string{}
	newStatus.Bots = []string{}

	for _, line := range lines[2:] {

		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ping := fields[1]
		name := ""
		if parts := strings.SplitN(line, "\"", 3); len(parts) >= 2 {
			name = parts[1]
		}

		if ping == "0" {
			newStatus.Bots = append(newStatus.Bots, name)
		} else {
			newStatus.Players = append(newStatus.Players, name)
		}
	}
	newStatus.PlayerCount = len(newStatus.Players)
	newStatus.BotCount = len(newStatus.Bots)

	serverMutex.Lock()
	s.Hostname = newStatus.Hostname
	s.Map = newStatus.Map
	s.Mod = newStatus.Mod
	s.GameType = newStatus.GameType
	s.Version = newStatus.Version
	s.PB = newStatus.PB
	s.MaxPlayers = newStatus.MaxPlayers
	s.Players = newStatus.Players
	s.PlayerCount = newStatus.PlayerCount
	s.LastSeen = newStatus.LastSeen
	s.Online = true
	s.Bots = newStatus.Bots
	s.BotCount = newStatus.BotCount
	serverMutex.Unlock()
}

func markOffline(s *ServerEntry) {
	serverMutex.Lock()
	s.Online = false
	serverMutex.Unlock()
}

func parseInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func serveAPI(w http.ResponseWriter, r *http.Request) {
	serverMutex.Lock()
	defer serverMutex.Unlock()

	list := make([]*ServerEntry, 0, len(serverList))
	for _, s := range serverList {
		list = append(list, s)
	}

	// Online servers first
	sort.Slice(list, func(i, j int) bool {
		// Primary: player count descending
		if list[i].PlayerCount != list[j].PlayerCount {
			return list[i].PlayerCount > list[j].PlayerCount
		}
		// Secondary: online status
		if list[i].Online != list[j].Online {
			return list[i].Online
		}
		// Fallback: alphabetical by address
		return list[i].Address < list[j].Address
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
