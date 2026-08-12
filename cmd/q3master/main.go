package main

import (
    "fmt"
    "net/http"
    "os"
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
    if err := servers.LoadOrSeed(stateFile, seedURL); err != nil {
        fmt.Printf("failed to load/seed server state: %v\n", err)
    }

    mongoURI := os.Getenv("MONGO_URI")
    mongoDB := os.Getenv("MONGO_DB")
    if err := history.Init(mongoURI, mongoDB); err != nil {
        fmt.Printf("failed to init player-count history: %v\n", err)
    }

    // background workers
    servers.StartPollWorkers(8)
    servers.StartDiscovery(5 * time.Minute)
    servers.StartPolling(15 * time.Second)
    servers.StartJanitor()
    servers.StartAutosave(stateFile, 2*time.Minute)
    servers.StartNetworkSampling(time.Minute, history.RecordNetworkSample)
    history.StartRollup(15 * time.Minute)
    // start UDP master server (getservers + heartbeat)
    servers.StartMasterUDP(":27950")

    // HTTP endpoints
    http.HandleFunc("/api/servers", httpapi.WithCORS(httpapi.ServeServersAPI))
    http.HandleFunc("/api/server", httpapi.WithCORS(httpapi.ServeServerAPI))
    http.HandleFunc("/api/master-status", httpapi.WithCORS(httpapi.ServeMasterStatusAPI))
    http.HandleFunc("/api/history", httpapi.WithCORS(httpapi.ServeHistoryAPI))
    http.HandleFunc("/api/history/network", httpapi.WithCORS(httpapi.ServeNetworkHistoryAPI))
    http.Handle("/", http.FileServer(http.Dir("web")))

    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    fmt.Println("Listening on :" + port)
    _ = http.ListenAndServe(":"+port, nil)
}
