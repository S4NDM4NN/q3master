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
    servers.PurgeIgnored()

    mongoURI := os.Getenv("MONGO_URI")
    mongoDB := os.Getenv("MONGO_DB")
    if err := history.Init(mongoURI, mongoDB); err != nil {
        fmt.Printf("failed to init player-count history: %v\n", err)
    }

    // background workers
    servers.StartPollWorkers(8)
    servers.StartDiscovery(5*time.Minute, history.RecordMasterSample)
    servers.StartPolling(15 * time.Second)
    servers.StartJanitor()
    servers.StartAutosave(stateFile, 2*time.Minute)
    servers.StartNetworkSampling(time.Minute, history.RecordNetworkSample)
    servers.StartCloneDetection(5*time.Minute, cloneGroupsFile)
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
    http.Handle("/", http.FileServer(http.Dir("web")))

    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    fmt.Println("Listening on :" + port)
    _ = http.ListenAndServe(":"+port, nil)
}
