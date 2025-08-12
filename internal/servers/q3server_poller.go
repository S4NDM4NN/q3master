package servers

import (
	"net"
	"strings"
	"sync"
	"time"
)

// --- Poll worker queue with de-duplication ---

var (
	pollQueue   chan string
	pendingPoll = make(map[string]bool)
	// dedicated mutex for poll queue state; do not alias serverMutex
	pollQueueMutex sync.Mutex
)

// StartPollWorkers spins up N workers to process poll requests.
func StartPollWorkers(n int) {
	if n <= 0 {
		n = 4
	}
	pollQueue = make(chan string, 1024)
	for i := 0; i < n; i++ {
		go func() {
			for addr := range pollQueue {
				// clear pending mark
				pollQueueMutex.Lock()
				delete(pendingPoll, addr)
				pollQueueMutex.Unlock()

				serverMutex.Lock()
				s := serverList[addr]
				serverMutex.Unlock()
				if s != nil {
					pollServer(s)
				}
			}
		}()
	}
}

// EnqueuePoll schedules a server for polling if not already pending.
func EnqueuePoll(addr string) {
	pollQueueMutex.Lock()
	// Only enqueue if not already pending
	if pendingPoll[addr] {
		pollQueueMutex.Unlock()
		return
	}
	// Try non-blocking enqueue; mark as pending only on success
	select {
	case pollQueue <- addr:
		pendingPoll[addr] = true
	default:
		// queue full; leave pending=false so future attempts can retry
	}
	pollQueueMutex.Unlock()
}

// StartPolling periodically polls servers for status.
func StartPolling(interval time.Duration) {
	go func() {
		for {
			pollServers()
			time.Sleep(interval)
		}
	}()
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
		EnqueuePoll(s.Address)
	}
}

func pollServer(s *ServerEntry) {
	serverMutex.Lock()
	s.Polls++
	s.LastAttempt = time.Now()
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
		case "protocol":
			// capture protocol if server reports it
			newStatus.Protocol = parseInt(v)
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
	s.LastGoodPoll = time.Now()
	s.MissedPolls = 0
	s.State = StateOnline
	if newStatus.Protocol != 0 {
		s.Protocol = newStatus.Protocol
	}
	serverMutex.Unlock()
}

func markOffline(s *ServerEntry) {
	serverMutex.Lock()
	defer serverMutex.Unlock()

	s.Online = false
	s.MissedPolls++
	if !s.LastGoodPoll.IsZero() {
		s.State = StateOffline
	} else {
		s.State = StateNew
	}
}
