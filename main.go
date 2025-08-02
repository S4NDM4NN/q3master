package main

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type GameServer struct {
	Addr     *net.UDPAddr
	LastSeen time.Time
}

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

var (
	serverList    = make(map[string]*GameServer)
	rateLimiters  = make(map[string]*clientLimiter)
	rlMutex       sync.Mutex
	serverTimeout = 3 * time.Minute
)

func main() {
	addr := net.UDPAddr{Port: 27950}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		panic(err)
	}
	fmt.Println("Master server listening on :27950")

	go pruneStaleServers()
	go cleanupRateLimiters()

	buffer := make([]byte, 2048)

	for {
		n, clientAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			fmt.Printf("Read error: %v\n", err)
			continue
		}

		ip := clientAddr.IP.String()
		limiter := getRateLimiter(ip)
		if !limiter.Allow() {
			fmt.Printf("Rate limit exceeded for %s\n", ip)
			continue
		}

		msg := string(buffer[:n])
		fmt.Printf("DEBUG: raw from %s: %q\n", clientAddr, msg)

		// Strip header if present
		msg = strings.TrimPrefix(msg, "\xff\xff\xff\xff")

		if strings.HasPrefix(msg, "heartbeat") {
			serverList[clientAddr.String()] = &GameServer{Addr: clientAddr, LastSeen: time.Now()}
			fmt.Printf("Heartbeat from %s\n", clientAddr)
		} else if strings.Contains(msg, "getservers") {
			fmt.Printf("getservers from %s\n", clientAddr)
			response := buildGetServersResponse()
			conn.WriteToUDP(response, clientAddr)
		}
	}
}

func getRateLimiter(ip string) *rate.Limiter {
	rlMutex.Lock()
	defer rlMutex.Unlock()

	entry, exists := rateLimiters[ip]
	if !exists {
		limiter := rate.NewLimiter(1, 3) // 1 per second, burst of 3
		rateLimiters[ip] = &clientLimiter{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

func cleanupRateLimiters() {
	for {
		time.Sleep(5 * time.Minute)
		rlMutex.Lock()
		for ip, entry := range rateLimiters {
			if time.Since(entry.lastSeen) > 10*time.Minute {
				delete(rateLimiters, ip)
			}
		}
		rlMutex.Unlock()
	}
}

func pruneStaleServers() {
	for {
		time.Sleep(1 * time.Minute)
		now := time.Now()
		for key, server := range serverList {
			if now.Sub(server.LastSeen) > serverTimeout {
				delete(serverList, key)
				fmt.Printf("Removed stale server %s\n", key)
			}
		}
	}
}

func buildGetServersResponse() []byte {
	buf := bytes.NewBuffer([]byte{0xff, 0xff, 0xff, 0xff})
	buf.WriteString("getserversResponse\n")

	for _, server := range serverList {
		ip := server.Addr.IP.To4()
		if ip == nil {
			continue // skip IPv6
		}
		port := uint16(server.Addr.Port)
		buf.Write(ip)
		buf.WriteByte(byte(port >> 8))
		buf.WriteByte(byte(port & 0xff))
	}
	buf.WriteByte(0x00) // EOT
	return buf.Bytes()
}
