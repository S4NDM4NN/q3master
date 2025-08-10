package main

import (
    "fmt"
    "net/http"
    "os"
    "time"

    "q3master/internal/httpapi"
    "q3master/internal/servers"
)

func main() {
    // background workers
    servers.StartDiscovery(5 * time.Minute)
    servers.StartPolling(15 * time.Second)
    servers.StartJanitor()

    // HTTP endpoints
    http.HandleFunc("/api/servers", httpapi.WithCORS(httpapi.ServeServersAPI))
    http.Handle("/", http.FileServer(http.Dir("web")))

    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    fmt.Println("Listening on :" + port)
    _ = http.ListenAndServe(":"+port, nil)
}

