package servers

import (
	"net"
	"strings"
	"sync"
	"time"

	"q3master/internal/history"
)

// --- Poll worker queue with de-duplication ---

var (
	pollQueue   chan string
	pendingPoll = make(map[string]bool)
	// dedicated mutex for poll queue state; do not alias serverMutex
	pollQueueMutex sync.Mutex
)

// --- Per-IP poll pacing ---

// minPollIntervalPerIP is the minimum spacing enforced between poll packets
// sent to the same IP. Some hosts run several servers (different ports) on
// one machine, and with 8 poll workers running concurrently those can
// otherwise all get queued and dispatched within the same instant --
// several simultaneous getstatus packets landing on one host looks a lot
// like the opening burst of a UDP scan/flood, and is exactly the kind of
// traffic a host-side firewall might start rate-limiting or blocking on
// (see offlineRetryBackoff above for the same concern applied to a single
// address). Staggering polls to a shared IP keeps our traffic looking like
// ordinary, spaced-out queries instead.
const minPollIntervalPerIP = 750 * time.Millisecond

var (
	ipPollMutex sync.Mutex
	ipLastPoll  = make(map[string]time.Time)
)

// waitForIPSlot blocks (if needed) until at least minPollIntervalPerIP has
// passed since the last poll dispatched to host, then reserves the slot.
func waitForIPSlot(host string) {
	for {
		ipPollMutex.Lock()
		now := time.Now()
		last, ok := ipLastPoll[host]
		if !ok || now.Sub(last) >= minPollIntervalPerIP {
			ipLastPoll[host] = now
			ipPollMutex.Unlock()
			return
		}
		wait := minPollIntervalPerIP - now.Sub(last)
		ipPollMutex.Unlock()
		time.Sleep(wait)
	}
}

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
		switch {
		case !s.Online:
			if now.Sub(s.LastAttempt) >= offlineRetryBackoff(s.MissedPolls) {
				toPoll = append(toPoll, s)
			}
		case now.Sub(s.LastSeen) > 2*time.Minute:
			toPoll = append(toPoll, s)
		}
	}
	serverMutex.Unlock()

	for _, s := range toPoll {
		EnqueuePoll(s.Address)
	}
}

// offlineRetryBaseBackoff/offlineRetryMaxBackoff bound how long we wait
// between re-polls of a server that's still offline. Without this, this
// sweep (every 15s, see StartPolling in main.go) re-queues *every* offline
// server on *every* tick forever -- for a server down several hours that's
// thousands of getstatus queries. getstatus is a known UDP amplification
// vector, so a source hammering it that hard is exactly the kind of traffic
// many RTCW/ET hosts (or an upstream firewall) rate-limit or block on sight
// -- which can turn a transient blip into a much longer outage purely
// because of how aggressively we were retrying it.
const (
	offlineRetryBaseBackoff = 15 * time.Second
	offlineRetryMaxBackoff  = 5 * time.Minute
)

// offlineRetryBackoff grows from offlineRetryBaseBackoff up to
// offlineRetryMaxBackoff as consecutive missed polls accumulate.
func offlineRetryBackoff(missedPolls int) time.Duration {
	level := missedPolls - offlineAfterMissedPolls
	if level < 0 {
		level = 0
	}
	if level > 10 { // avoid an oversized shift; well past the backoff cap anyway
		level = 10
	}
	delay := offlineRetryBaseBackoff * time.Duration(int64(1)<<uint(level))
	if delay > offlineRetryMaxBackoff {
		delay = offlineRetryMaxBackoff
	}
	return delay
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

	waitForIPSlot(addr.IP.String())

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		retryIfProvisional(s, markOffline(s))
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = conn.Write([]byte("\xff\xff\xff\xffgetstatus\n"))
	if err != nil {
		retryIfProvisional(s, markOffline(s))
		return
	}

	buffer := make([]byte, 4096)
	n, err := conn.Read(buffer)
	if err != nil || n == 0 {
		retryIfProvisional(s, markOffline(s))
		return
	}

	lines := strings.Split(string(buffer[:n]), "\n")
	if len(lines) < 2 {
		retryIfProvisional(s, markOffline(s))
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
		case "Score_Time":
			if secs, ok := parseMatchTime(v); ok {
				newStatus.MatchTimeSec = secs
				newStatus.HasMatchTime = true
			}
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
	s.MatchTimeSec = newStatus.MatchTimeSec
	s.HasMatchTime = newStatus.HasMatchTime
	s.LastGoodPoll = time.Now()
	s.MissedPolls = 0
	s.State = StateOnline
	if newStatus.Protocol != 0 {
		s.Protocol = newStatus.Protocol
	}
	serverMutex.Unlock()

	history.RecordSample(s.Address, newStatus.PlayerCount, newStatus.MaxPlayers, newStatus.Map)
}

// offlineAfterMissedPolls is how many consecutive failed polls a
// previously-online server must rack up before it's actually marked
// offline. UDP is lossy, so a single dropped/timed-out getstatus request
// isn't reliable evidence a server actually went down -- without this,
// one missed packet on a populous server would yank its whole player
// count out of (and back into) the network-wide totals every time it
// happened, even though nothing really changed.
const offlineAfterMissedPolls = 2

// retryDelay is how long to wait before re-polling a server that just
// missed a poll but hasn't hit offlineAfterMissedPolls yet. Without this,
// the next attempt would only come from the passive sweep in
// pollServers(), which for a server that just looked healthy (recent
// LastSeen) can be up to ~2 minutes away -- too slow to tell a genuine
// blip apart from a real outage.
const retryDelay = 5 * time.Second

// markOffline records a failed poll and reports whether the server is
// still within its grace period (true) or was actually flipped offline
// (false).
func markOffline(s *ServerEntry) bool {
	serverMutex.Lock()
	defer serverMutex.Unlock()

	s.MissedPolls++
	if !s.LastGoodPoll.IsZero() {
		// Was online before: give it offlineAfterMissedPolls consecutive
		// misses before actually flipping it offline.
		if s.MissedPolls >= offlineAfterMissedPolls {
			s.Online = false
			s.State = StateOffline
			return false
		}
		return true
	}
	// Never had a good poll: no "online" status to protect, so mark it
	// immediately (existing StateNew eviction in janitor.go still applies
	// at 10 missed polls).
	s.Online = false
	s.State = StateNew
	return false
}

// retryIfProvisional schedules a fast re-poll for a server still within
// its offline grace period, instead of waiting on the passive sweep.
func retryIfProvisional(s *ServerEntry, stillProvisional bool) {
	if !stillProvisional {
		return
	}
	time.AfterFunc(retryDelay, func() {
		EnqueuePoll(s.Address)
	})
}
