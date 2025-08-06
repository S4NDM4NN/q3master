package main

import (
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
	serverMutex   sync.Mutex
	rateLimiters  = make(map[string]*clientLimiter)
	rlMutex       sync.Mutex
	serverTimeout = 3 * time.Minute
)

func startMasterServer() {
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
		msg = strings.TrimPrefix(msg, "\xff\xff\xff\xff")

		if strings.HasPrefix(msg, "heartbeat") {
			serverMutex.Lock()
			serverList[clientAddr.String()] = &GameServer{
				Addr:     clientAddr,
				LastSeen: time.Now(),
			}
			serverMutex.Unlock()
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
		limiter := rate.NewLimiter(1, 3) // 1 per second, burst 3
		rateLimiters[ip] = &clientLimiter{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

func pruneStaleServers() {
	for {
		time.Sleep(1 * time.Minute)
		serverMutex.Lock()
		now := time.Now()
		for key, server := range serverList {
			if now.Sub(server.LastSeen) > serverTimeout {
				delete(serverList, key)
				fmt.Printf("Removed stale server %s\n", key)
			}
		}
		serverMutex.Unlock()
	}
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

func buildGetServersResponse() []byte {
	buf := make([]byte, 0, 4096)
	buf = append(buf, 0xff, 0xff, 0xff, 0xff)
	buf = append(buf, []byte("getserversResponse\n")...)

	serverMutex.Lock()
	defer serverMutex.Unlock()

	for _, server := range serverList {
		ip := server.Addr.IP.To4()
		if ip == nil {
			continue
		}
		port := uint16(server.Addr.Port)
		buf = append(buf, ip[0], ip[1], ip[2], ip[3])
		buf = append(buf, byte(port>>8), byte(port&0xff))
	}
	buf = append(buf, 0x00) // EOT
	return buf
}
