package httpapi

import (
    "encoding/json"
    "net/http"
    "sort"

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

