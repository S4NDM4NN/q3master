package main

import (
    "fmt"
    "net/http"
    "os"
    "path/filepath"
    "time"

    "q3master/internal/history"
    "q3master/internal/httpapi"
    "q3master/internal/servers"
)

func main() {
    stateFile := os.Getenv("SERVER_STATE_FILE")
    if stateFile == "" {
        stateFile = "data/servers.json"
    }
    seedURL := os.Getenv("SERVER_SEED_URL")
    if seedURL == "" {
        seedURL = "https://list.s4ndmod.com/api/servers"
    }
    // Detected clone groups (see servers.StartCloneDetection) live alongside
    // the main state file but are persisted separately, so a format change
    // in one can't break loading the other.
    cloneGroupsFile := filepath.Join(filepath.Dir(stateFile), "clone_groups.json")

    if err := servers.LoadCloneGroups(cloneGroupsFile); err != nil {
        fmt.Printf("failed to load clone-group state: %v\n", err)
    }
    if err := servers.LoadOrSeed(stateFile, seedURL); err != nil {
        fmt.Printf("failed to load/seed server state: %v\n", err)
    }
    // Loaded/seeded ServerEntry values can carry AlsoKnownAs (it's just
    // another JSON field) without cloneGroups/aliasToPrimary knowing about
    // it -- e.g. a freshly-seeded instance with no local clone_groups.json
    // yet. Reconstruct from what was loaded so the port-padding dashboard,
    // isKnownAlias, and everything downstream of it stay consistent with
    // what the cards already show.
    servers.RebuildCloneGroupsFromServerState()
    // Catches clone groups whose primary was evicted (offline 7+ days, or
    // 10+ missed polls while never-online) at some point before this
    // startup -- StartJanitor's own cleanup only ever sees evictions from
    // here on, not ones that already happened in a previous run.
    servers.ReleaseAlreadyOrphanedGroups()
    servers.PurgeIgnored()

    mongoURI := os.Getenv("MONGO_URI")
    mongoDB := os.Getenv("MONGO_DB")
    if err := history.Init(mongoURI, mongoDB); err != nil {
        fmt.Printf("failed to init player-count history: %v\n", err)
    }

    // background workers
    // Raised from 8 on 2026-08-16: with ~1900 known servers (many sharing
    // an IP, so waitForIPSlot's 750ms per-IP pacing -- unaffected by worker
    // count -- is what actually bounds traffic to any one target) 8
    // workers couldn't keep up, leaving up to 62% of currently-online
    // servers overdue for repoll and some flipping to "offline" purely from
    // worker starvation rather than actually going down. More workers only
    // adds parallelism *across* distinct IPs, not burst traffic to any
    // single one.
    servers.StartPollWorkers(100)
    servers.StartDiscovery(5*time.Minute, history.RecordMasterSample)
    servers.StartPolling(15 * time.Second)
    servers.StartJanitor()
    servers.StartAutosave(stateFile, 2*time.Minute)
    servers.StartNetworkSampling(time.Minute, history.RecordNetworkSample)
    servers.StartCloneDetection(5*time.Minute, cloneGroupsFile)
    servers.StartAliasRecheck()
    history.StartRollup(15 * time.Minute)
    // start UDP master server (getservers + heartbeat)
    servers.StartMasterUDP(":27950")

    // HTTP endpoints
    http.HandleFunc("/api/servers", httpapi.WithCORS(httpapi.ServeServersAPI))
    http.HandleFunc("/api/server", httpapi.WithCORS(httpapi.ServeServerAPI))
    http.HandleFunc("/api/master-status", httpapi.WithCORS(httpapi.ServeMasterStatusAPI))
    http.HandleFunc("/api/master-status/all", httpapi.WithCORS(httpapi.ServeAllMasterStatusAPI))
    http.HandleFunc("/api/history", httpapi.WithCORS(httpapi.ServeHistoryAPI))
    http.HandleFunc("/api/history/network", httpapi.WithCORS(httpapi.ServeNetworkHistoryAPI))
    http.HandleFunc("/api/history/master/daily", httpapi.WithCORS(httpapi.ServeMasterDailyUptimeAPI))
    http.HandleFunc("/api/ignored", httpapi.WithCORS(httpapi.ServeIgnoredAPI))
    http.HandleFunc("/api/port-padding", httpapi.WithCORS(httpapi.ServePortPaddingAPI))
    http.HandleFunc("/api/port-padding/recheck", httpapi.WithCORS(httpapi.ServeRecheckPortPaddingAPI))
    http.Handle("/", http.FileServer(http.Dir("web")))

    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    fmt.Println("Listening on :" + port)
    _ = http.ListenAndServe(":"+port, nil)
}
