package httpapi

import (
    "encoding/json"
    "net/http"
    "sort"

    "q3master/internal/history"
    "q3master/internal/servers"
)

// ServeServersAPI responds with the list of servers in JSON.
func ServeServersAPI(w http.ResponseWriter, r *http.Request) {
    list := servers.ListServers()

    // Online servers first, then by player count desc, then address
    sort.Slice(list, func(i, j int) bool {
        if list[i].PlayerCount != list[j].PlayerCount {
            return list[i].PlayerCount > list[j].PlayerCount
        }
        if list[i].Online != list[j].Online {
            return list[i].Online
        }
        return list[i].Address < list[j].Address
    })

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(list)
}

// ServeServerAPI responds with a single server's details. Query params:
// address (required, "ip:port"). 404s if the server isn't known.
func ServeServerAPI(w http.ResponseWriter, r *http.Request) {
    address := r.URL.Query().Get("address")
    if address == "" {
        http.Error(w, "missing address parameter", http.StatusBadRequest)
        return
    }

    s, ok := servers.GetServer(address)
    if !ok {
        http.Error(w, "server not found", http.StatusNotFound)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(s)
}

// ServeMasterStatusAPI responds with the reachability of the real master.
func ServeMasterStatusAPI(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(servers.GetMasterStatus())
}

// ServeHistoryAPI responds with player-count history for a single server.
// Query params: address (required, "ip:port"), range ("7d" | "30d" | "all",
// default "7d").
func ServeHistoryAPI(w http.ResponseWriter, r *http.Request) {
    address := r.URL.Query().Get("address")
    if address == "" {
        http.Error(w, "missing address parameter", http.StatusBadRequest)
        return
    }
    since := history.ParseRange(r.URL.Query().Get("range"))

    points, err := history.GetServerHistory(r.Context(), address, since)
    if err != nil {
        http.Error(w, "failed to load history", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(points)
}

// ServeNetworkHistoryAPI responds with network-wide history: total real
// players and online server count over time. Query params: range ("7d" |
// "30d" | "all", default "7d"), protocol (a Q3 protocol number e.g. "84", or
// "all" for the combined fleet, default "all").
func ServeNetworkHistoryAPI(w http.ResponseWriter, r *http.Request) {
    since := history.ParseRange(r.URL.Query().Get("range"))
    protocol := r.URL.Query().Get("protocol")

    points, err := history.GetNetworkHistory(r.Context(), protocol, since)
    if err != nil {
        http.Error(w, "failed to load network history", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(points)
}

