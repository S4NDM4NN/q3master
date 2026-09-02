package servers

import (
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"q3master/internal/history"
)

// --- Poll worker queues with de-duplication ---
//
// Due polls are split into two priority queues so the much larger
// backoff-driven offline-retry population can never crowd out a
// latency-sensitive refresh of a server we currently believe is online.
// Before this split, a burst of offline retries (or just ordinary UDP
// jitter) could delay an online server's refresh past janitor.go's
// 5-minute LastSeen cutoff, force-flipping it offline -- which fed straight
// back into the offline-retry population as another burst, a
// self-reinforcing decline that raising worker count alone didn't fix.
// Workers always drain onlinePollQueue first (see nextPoll).
var (
	onlinePollQueue  chan string
	offlinePollQueue chan string
	pendingPoll      = make(map[string]bool)
	// dedicated mutex for poll queue state; do not alias serverMutex
	pollQueueMutex sync.Mutex
)

// Queue capacities are sized independently of worker count and of each
// other: onlinePollQueue only ever needs to hold currently-online servers
// due for refresh (a few hundred at a time), while offlinePollQueue absorbs
// the much larger backoff-driven retry population (thousands of
// long-known-dead addresses). Undersizing either just means EnqueuePoll's
// non-blocking send drops the entry for this tick -- it's retried on the
// next pollServers() sweep -- but previously both shared one 1024-slot
// queue, so a deep offline backlog could starve online enqueues outright.
const (
	onlinePollQueueCapacity  = 4096
	offlinePollQueueCapacity = 16384
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

// onlinePollTimeout/offlineProbeTimeout bound how long a single getstatus
// request waits for a reply. Online refreshes get the full budget since
// these are servers we expect to answer and want a fair chance against
// ordinary packet loss. Offline probes get a much shorter budget: most fail
// outright with no reply at all (UDP gives no fast "connection refused" for
// a silently-dropped or dead host), so a probe that's going to fail doesn't
// need to hold a worker for as long as one we expect to succeed.
const (
	onlinePollTimeout   = 3 * time.Second
	offlineProbeTimeout = 1200 * time.Millisecond
)

// StartPollWorkers spins up N workers to process poll requests. Each worker
// always prefers onlinePollQueue over offlinePollQueue (see nextPoll), so
// the bulk offline-retry population can never delay a due online refresh.
func StartPollWorkers(n int) {
	if n <= 0 {
		n = 4
	}
	onlinePollQueue = make(chan string, onlinePollQueueCapacity)
	offlinePollQueue = make(chan string, offlinePollQueueCapacity)
	for i := 0; i < n; i++ {
		go func() {
			for {
				addr, timeout := nextPoll()
				dispatchPoll(addr, timeout)
			}
		}()
	}
}

// nextPoll blocks until a poll is due, always preferring onlinePollQueue:
// the first select is a non-blocking check so a pending online poll is
// picked up even if the second select would otherwise pick offlinePollQueue
// at random between two ready channels.
func nextPoll() (addr string, timeout time.Duration) {
	select {
	case addr := <-onlinePollQueue:
		return addr, onlinePollTimeout
	default:
	}
	select {
	case addr := <-onlinePollQueue:
		return addr, onlinePollTimeout
	case addr := <-offlinePollQueue:
		return addr, offlineProbeTimeout
	}
}

func dispatchPoll(addr string, timeout time.Duration) {
	// clear pending mark
	pollQueueMutex.Lock()
	delete(pendingPoll, addr)
	pollQueueMutex.Unlock()

	serverMutex.Lock()
	s := serverList[addr]
	serverMutex.Unlock()
	if s != nil {
		pollServer(s, timeout)
	}
}

// EnqueuePoll schedules addr for a high-priority (online-refresh) poll if
// not already pending. Used for anything latency-sensitive: brand-new
// addresses on first contact, provisional-offline retries still in their
// grace period, and released clone-group aliases. The bulk backoff-driven
// offline-retry population goes through enqueueOfflinePoll instead, so it
// can never crowd these out -- see the queue declarations above.
func EnqueuePoll(addr string) {
	enqueuePoll(addr, onlinePollQueue)
}

// enqueueOfflinePoll schedules addr for a low-priority offline-retry poll.
// Only pollServers' backoff-driven sweep should call this.
func enqueueOfflinePoll(addr string) {
	enqueuePoll(addr, offlinePollQueue)
}

func enqueuePoll(addr string, queue chan string) {
	pollQueueMutex.Lock()
	// Only enqueue if not already pending
	if pendingPoll[addr] {
		pollQueueMutex.Unlock()
		return
	}
	// Try non-blocking enqueue; mark as pending only on success
	select {
	case queue <- addr:
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
	var toPollOnline, toPollOffline []*ServerEntry

	for _, s := range serverList {
		switch {
		case !s.Online:
			if now.Sub(s.LastAttempt) >= offlineRetryBackoff(s.MissedPolls) {
				toPollOffline = append(toPollOffline, s)
			}
		case now.Sub(s.LastSeen) > 2*time.Minute:
			toPollOnline = append(toPollOnline, s)
		}
	}
	serverMutex.Unlock()

	for _, s := range toPollOnline {
		EnqueuePoll(s.Address)
	}
	for _, s := range toPollOffline {
		enqueueOfflinePoll(s.Address)
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
// offlineRetryMaxBackoff as consecutive missed polls accumulate, plus up to
// +/-15% jitter. Without the jitter, a batch of servers that all went
// offline around the same moment (e.g. a wave of janitor evictions) would
// all come due for retry in lockstep on every subsequent rung -- exactly
// the kind of synchronized burst that crowds out concurrently-due online
// refreshes, even with the two queues split (see the queue declarations
// above).
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
	jitterFactor := 0.85 + rand.Float64()*0.3 // [0.85, 1.15)
	return time.Duration(float64(delay) * jitterFactor)
}

// queryGetStatus sends a getstatus request directly to address and parses
// the response into a bare ServerEntry-shaped snapshot (Hostname/Map/.../
// Players/Bots/PlayerCount/BotCount/MatchTimeSec/HasMatchTime, LastSeen set
// to now), without touching serverList. Used by pollServer below (which
// then copies the result onto the tracked entry) and by the clone-alias
// recheck path (clone_recheck.go), which needs to query an address that was
// deliberately removed from serverList once folded in as an alias --
// ordinary polling never sees it again, so it has no ServerEntry of its own
// to poll through. Applies the same per-IP pacing (waitForIPSlot) as
// ordinary polling either way, so recheck traffic looks the same to a
// remote host as any other poll. timeout bounds how long this one request
// waits for a reply -- see onlinePollTimeout/offlineProbeTimeout.
func queryGetStatus(address string, timeout time.Duration) (*ServerEntry, bool) {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, false
	}

	waitForIPSlot(addr.IP.String())

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, false
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte("\xff\xff\xff\xffgetstatus\n")); err != nil {
		return nil, false
	}

	buffer := make([]byte, 4096)
	n, err := conn.Read(buffer)
	if err != nil || n == 0 {
		return nil, false
	}

	lines := strings.Split(string(buffer[:n]), "\n")
	if len(lines) < 2 {
		return nil, false
	}

	keyValues := strings.Split(strings.TrimPrefix(lines[1], "\\"), "\\")
	status := &ServerEntry{
		LastSeen: time.Now(),
	}

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
		case "protocol":
			// capture protocol if server reports it
			status.Protocol = parseInt(v)
		case "Score_Time":
			if secs, ok := parseMatchTime(v); ok {
				status.MatchTimeSec = secs
				status.HasMatchTime = true
			}
		}
	}

	status.Players = []string{}
	status.Bots = []string{}

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
			status.Bots = append(status.Bots, name)
		} else {
			status.Players = append(status.Players, name)
		}
	}
	status.PlayerCount = len(status.Players)
	status.BotCount = len(status.Bots)

	return status, true
}

func pollServer(s *ServerEntry, timeout time.Duration) {
	serverMutex.Lock()
	s.Polls++
	s.LastAttempt = time.Now()
	serverMutex.Unlock()

	newStatus, ok := queryGetStatus(s.Address, timeout)
	if !ok {
		retryIfProvisional(s, markOffline(s))
		return
	}

	suspectedBots := updatePlayerSessions(s.Address, newStatus.Players)

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
	s.SuspectedBots = suspectedBots
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
			clearPlayerSessions(s.Address)
			return false
		}
		return true
	}
	// Never had a good poll: no "online" status to protect, so mark it
	// immediately (existing StateNew eviction in janitor.go still applies
	// at 10 missed polls).
	s.Online = false
	s.State = StateNew
	clearPlayerSessions(s.Address)
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
