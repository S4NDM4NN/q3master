package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
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

	list := make([]*ServerStatus, 0, len(statusCache))
	for _, s := range statusCache {
		list = append(list, s)
	}

	// Sort by player count descending
	sort.Slice(list, func(i, j int) bool {
		return list[i].PlayerCount > list[j].PlayerCount
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
