# Quake 3 Engine Server Browser

A standalone Go-based real-time server browser for Quake III Arena engine. This tool discovers, polls, and displays active game servers for Return to Castle Wolfenstein (RTCW), and Enemy Territory (ET) via a Bootstrap-powered frontend.

Live Demo: [click here](https://list.s4ndmod.com/)

---

## Features

**Master Server Polling** – Queries the official master server (`wolfmaster.idsoftware.com`) for active servers.
**Built-in Master UDP** – Serves Quake 3 `getservers` and accepts `heartbeat` on UDP 27950.
**Smart Poller** – Continuously polls servers for real-time status updates.
**Web API** – Exposes a `/api/servers` endpoint for consuming live server data.
**Bootstrap Frontend** – dark-themed interface with filtering, status indicators, player lists, and colorized nicknames.

---

## Getting Started

### Prerequisites

* Go 1.23+
* Docker (optional, for containerized builds)

---

### Build from Source

```bash
git clone https://github.com/youruser/q3master.git
cd q3master
go build -o q3master ./cmd/q3master
./q3master
```

The app will start polling servers and launch the web viewer at `http://localhost:8080`.

---

### Run with Docker

```bash
docker build -t q3master .
docker run -p 8080:8080 -p 27950:27950/udp q3master
```

---

## API

### `GET /api/servers`

Returns a JSON array of all discovered game servers.

Each object includes:

* `address`: IP and port
* `hostname`: Server name (Q3 color codes supported)
* `map`: Current map
* `mod`: Mod name (e.g., shrubet, omod)
* `gametype`: Gametype string
* `version`: Server version
* `pb`: PunkBuster status
* `player_count`: Number of real players
* `players[]`: Array of real player names
* `bot_count`: Number of bots
* `bots[]`: Array of bot names
* `last_seen`: Timestamp of last successful poll
* `online`: Boolean status
* `protocol`: Integer protocol version (57/60/84)

---

## Master UDP

- Listens on UDP `:27950` and responds to `getservers <protocol> ...` with `getserversResponse` containing known servers (merged from official master and heartbeats).
- Accepts `heartbeat` from game servers (adds/refreshes entry) and `shutdown` (removes entry). Heartbeat-sourced servers are polled immediately to enrich info and determine protocol.

---

## Web Viewer

The frontend is served at `/`. It includes:

* Auto-refresh every 10 seconds
* Filter/search by name, mod, map, IP, version
* Q3 Colored player and bot lists
* Protocol filter tabs
* Click-to-copy IP
* 🟢/🔴 Status indicators with "last seen" info

---

## Project Structure

```
q3master/
├── cmd/
│   └── q3master/
│       └── main.go           # Application entrypoint (HTTP server wiring)
├── internal/
│   ├── servers/              # Master UDP, discovery, polling, janitor, types, store
│   │   ├── master.go
│   │   ├── q3master_server.go
│   │   ├── q3master_poller.go
│   │   ├── janitor.go
│   │   ├── types.go
│   │   ├── list.go
│   │   └── util.go
│   └── httpapi/              # HTTP handlers and middleware
│       ├── handlers.go
│       └── middleware.go
├── web/
│   └── index.html            # HTML, CSS, JS (Bootstrap 5, jQuery)
├── go.mod / go.sum           # Dependencies
└── Dockerfile                # Build/run container image
```
