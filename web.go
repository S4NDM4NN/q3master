package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func startWebServer() {
	http.HandleFunc("/api/servers", handleAPIServers)
	http.Handle("/", http.FileServer(http.Dir("web")))

	port := "8080"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	http.ListenAndServe(":"+port, nil)
}

func handleAPIServers(w http.ResponseWriter, r *http.Request) {
	statusMutex.Lock()
	defer statusMutex.Unlock()

	fmt.Printf("Returning %d servers in /api/servers\n", len(statusCache)) // <-- ADD THIS LINE

	list := make([]*ServerStatus, 0, len(statusCache))
	for _, s := range statusCache {
		list = append(list, s)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
