package httpapi

import (
    "encoding/json"
    "net/http"
    "sort"
    "strconv"

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

// ServeAllMasterStatusAPI responds with the current reachability of every
// known master server (see servers.knownMasters), for the multi-master
// status page.
func ServeAllMasterStatusAPI(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(servers.GetAllMasterStatuses())
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

    points, err := history.GetNetworkHistory(r.Context(), protocol, since, servers.AliasAddresses())
    if err != nil {
        http.Error(w, "failed to load network history", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(points)
}

// ServeMasterDailyUptimeAPI responds with one uptime point per calendar day
// for the trailing N days, powering a status-page-style day-by-day uptime
// bar for one master server. Query params: host (required, e.g.
// "wolfmaster.idsoftware.com:27950" -- see servers.knownMasters), days
// (default 90).
func ServeMasterDailyUptimeAPI(w http.ResponseWriter, r *http.Request) {
    host := r.URL.Query().Get("host")
    if host == "" {
        http.Error(w, "missing host parameter", http.StatusBadRequest)
        return
    }

    days := 90
    if v := r.URL.Query().Get("days"); v != "" {
        if n, err := strconv.Atoi(v); err == nil && n > 0 {
            days = n
        }
    }

    points, err := history.GetMasterDailyUptime(r.Context(), host, days)
    if err != nil {
        http.Error(w, "failed to load master daily uptime", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(points)
}

// ServePollHealthAPI responds with how far behind the poll-worker pool is on
// repolling currently-online servers, broken down by protocol (see
// servers.GetPollHealth) -- a proactive gauge for the worker-starvation
// failure mode described in main.go's poll-worker-count comment, so it can
// be watched for directly instead of only noticed after the fact via
// visibly wrong offline counts.
func ServePollHealthAPI(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(servers.GetPollHealth())
}

// ServeIgnoredAPI responds with every blocked IP (see servers.ignoredHosts)
// and the specific addresses observed trying to register under it.
func ServeIgnoredAPI(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(servers.GetIgnoredHosts())
}

// ServePortPaddingAPI responds with every clone group broadcasting from
// multiple ports on one IP (see servers.GetPortPaddingGroups).
func ServePortPaddingAPI(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(servers.GetPortPaddingGroups())
}

// ServeRecheckPortPaddingAPI kicks off an immediate re-verification of
// every alias in one detected clone group (see servers.TriggerGroupRecheck)
// -- restricted to already-known padding groups, never an arbitrary
// address, and rate-limited per group. POST only. Query param: primary
// (required, must exactly match a currently-detected clone group's primary
// address).
func ServeRecheckPortPaddingAPI(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "POST required", http.StatusMethodNotAllowed)
        return
    }
    primary := r.URL.Query().Get("primary")
    if primary == "" {
        http.Error(w, "missing primary parameter", http.StatusBadRequest)
        return
    }

    queued, cooldown, err := servers.TriggerGroupRecheck(primary)
    if err != nil {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    if cooldown > 0 {
        w.WriteHeader(http.StatusTooManyRequests)
        _ = json.NewEncoder(w).Encode(map[string]any{
            "status":              "cooldown",
            "retry_after_seconds": int(cooldown.Seconds()),
        })
        return
    }
    _ = json.NewEncoder(w).Encode(map[string]any{
        "status":  "started",
        "primary": primary,
        "queued":  queued,
    })
}

