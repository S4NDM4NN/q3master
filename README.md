# Quake 3 Engine Server Browser

A standalone Go-based real-time server browser for id Tech 3 (Quake III engine) games. This tool discovers, polls, and displays active game servers for **Return to Castle Wolfenstein (RTCW)**, **Wolfenstein: Enemy Territory (ET)**, **Quake 3 Arena**, and **OpenArena** via a Bootstrap-powered frontend.

Live Server Viewer: [list.s4ndmod.com](https://list.s4ndmod.com)

---

## Adding Your Server to the List

To send direct heartbeats and keep your server visible even if a game's official master is down, add the following to your **RTCW/ET** server configuration:

```
set sv_master5 wolfmaster.s4ndmod.com
```

Quake 3 Arena and OpenArena servers should add this project's own master alongside the other community masters this tool already queries — see [Add master servers to your own server](https://list.s4ndmod.com/add-master.html) for the exact `sv_masterN` config for each game, since RTCW/ET and Q3A/OpenArena default to different official masters and don't share one config block.

---

## Features

* **Multi-Game, Multi-Protocol** – Tracks RTCW (protocols 57/60/61, including iortcw builds), ET (84, plus 82 for ET-mod forks that bump the wire protocol), Quake 3 Arena (68), and OpenArena (71). Servers whose protocol we can't classify, or that never report one at all, still show up under an "Unknown" filter with best-effort classification from their self-reported version string.
* **Multi-Master Discovery** – Queries the official id Software master alongside several community masters (`master.iortcw.org`, `etmaster.net`, `dpmaster.deathmask.net`, `master.ioquake3.org`, `master.maverickservers.com`, `master0.excessiveplus.net`) every 5 minutes, so an outage on any single master doesn't blank the list. Each server's "Learned via" shows every master (and/or direct heartbeat) that reported it.
* **Built-in Master UDP** – Serves Quake 3 `getservers` requests and accepts `heartbeat`/`shutdown` messages on UDP port 27950.
* **Direct Heartbeat Integration** – Combines discovery results with servers sending direct heartbeats straight to this project's own master, so listings stay resilient even if every upstream master is down.
* **Clone/Duplicate Collapsing** – Automatically detects a server broadcasting itself across many ports (or even several IPs) to inflate its apparent presence on the list, and collapses those into a single entry with an honest "also broadcasting as" note — rather than either counting it N times or silently hiding it. Detection layers by how much signal is available: real-player roster match (works even if the operator varies the hostname), bot-only roster + hostname match, and — for empty servers — a Quake 3 Arena–specific match-time-clock consistency check. A mirror across genuinely different IPs is treated as tolerated; a clone group whose extra addresses share the primary's own IP is **port padding** — flagged with a warning icon on its card and listed on the `port-padding.html` dashboard (still shown once and still served, just called out).
* **Self-Correcting Pairings** – Every detected duplicate is periodically re-verified with a fresh direct check of its own — starting at 15 minutes for a brand-new pairing and backing off toward once a day as it keeps holding up on repeated checks (a manual "recheck now" button on the padding dashboard can also force this immediately, restricted to already-detected groups). A pairing that no longer matches (an operator's IP now hosts something else, or a rare false match) is automatically unpaired and returned to the list as its own independent entry instead of staying wrong forever. The same applies if a group's primary itself disappears (evicted after being offline too long): its aliases are released back to normal circulation rather than being stranded forever, both as it happens and retroactively at startup for anything already orphaned.
* **Suspected-Bot Flagging** – Some servers spoof bots as real players (nonzero ping) to inflate the apparent player count. Names that stay connected continuously, without ever leaving the roster, past a generous threshold get flagged as suspected bots in the player list.
* **Player/Network History** – Optional MongoDB-backed history (see Configuration below): per-server player-count charts, network-wide total-players/online-servers charts (correctable for known clone groups after the fact, without ever rewriting stored history), and a rolling uptime bar for the official master.
* **Manual + Automatic Abuse Handling** – A small curated block-list for hosts doing something worse than ordinary list-padding, visible at `/ignored.html`.
* **Bootstrap Frontend** – Dark-themed, centered layout with a shareable-by-URL filter bar (game, and multi-select version/mod filters with in-dropdown search), colorized Q3 nicknames, and per-server detail pages.

---

## Getting Started

### Prerequisites

* Go 1.23+
* Docker (optional, for containerized builds)
* MongoDB (optional — only needed for player/network history charts and master-uptime tracking; the app runs fine without it)

---

### Build from Source

```bash
git clone https://github.com/S4NDM4NN/q3master.git
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

### Configuration

All configuration is via environment variables; every one is optional.

| Variable             | Default                                  | Purpose                                                                                     |
|----------------------|-------------------------------------------|-----------------------------------------------------------------------------------------------|
| `PORT`               | `8080`                                    | HTTP port for the web viewer and API.                                                        |
| `SERVER_STATE_FILE`  | `data/servers.json`                       | Where in-memory server state is periodically autosaved and loaded from on startup.           |
| `SERVER_SEED_URL`    | `https://list.s4ndmod.com/api/servers`    | On first run (no state file yet), seed the list from another running instance's API.         |
| `MONGO_URI`          | *(unset — history disabled)*              | Connection string for the optional player/network/master-uptime history feature.             |
| `MONGO_DB`           | `q3master`                                | Database name to use when `MONGO_URI` is set.                                                |

---

## API

### `GET /api/servers`

Returns a JSON array of all discovered game servers (already deduplicated — see Clone/Duplicate Collapsing above).

Each object includes:

* `address`: IP and port
* `hostname`: Server name (Q3 color codes supported)
* `map`: Current map
* `mod`: Mod name (e.g., shrubet, jaymod, silEnT, baseq3)
* `gametype`: Gametype string
* `version`: Server's self-reported version string
* `pb`: PunkBuster status
* `player_count` / `max_players`: Real player count and slot cap
* `players[]`: Array of real player names
* `bot_count` / `bots[]`: Bot count and names (ping-0 entries)
* `suspected_bots[]`: Names from `players[]` heuristically flagged as spoofed bots (see Suspected-Bot Flagging)
* `protocol`: Integer protocol version (57/60/61/68/71/82/84, or 0 if never reported)
* `state`: `new` / `online` / `offline`
* `online`: Boolean status
* `first_seen` / `last_seen` / `last_attempt` / `last_good_poll`: Lifecycle timestamps
* `missed_polls` / `polls`: Poll bookkeeping
* `last_heartbeat` / `heartbeat_count`: Direct-heartbeat observability
* `sources`: Map of master label (or the direct-heartbeat pseudo-source) to when it last reported this server
* `also_known_as[]`: Other addresses collapsed into this entry (see Clone/Duplicate Collapsing), each with the hostname/protocol it was reporting when folded in
* `match_time_sec` / `has_match_time`: Quake 3 Arena–family "elapsed match time" (Score_Time), when the server reports it

### `GET /api/server?address=ip:port`

Returns a single server's full detail object (same shape as above), or 404.

### `GET /api/master-status` / `GET /api/master-status/all`

Live up/down status for the official id Software master, or every known master respectively.

### `GET /api/history?address=ip:port&range=7d|30d|all`

Per-server player-count history (requires `MONGO_URI`).

### `GET /api/history/network?range=7d|30d|all&protocol=<num|all>`

Network-wide total-players/online-servers history, optionally scoped to one protocol (requires `MONGO_URI`).

### `GET /api/history/master/daily?host=<master host>&days=<n>`

Daily uptime percentage for a given master, for status-page uptime bars (requires `MONGO_URI`).

### `GET /api/ignored`

The curated block-list of hosts excluded from the listing entirely (see Manual + Automatic Abuse Handling).

### `GET /api/port-padding`

Every detected clone group (see Clone/Duplicate Collapsing above) with at least one IP holding 2+ of its own addresses — one row per detected clone, matching its one card on the main list, broken down by every IP its own membership (primary and aliases alike) actually clusters on rather than just whichever address was chosen as the group's primary. Each alias also reports its ongoing re-verification status (see Self-Correcting Pairings above): `first_paired`, `last_checked`, `check_count`.

### `POST /api/port-padding/recheck?primary=<ip:port>`

Immediately re-verifies every alias in one detected clone group, restricted to `primary` addresses that are already a known padding group (never an arbitrary address) and rate-limited to once every 2 minutes per group. Runs in the background; responds right away with how many addresses were queued.

---

## Master UDP

* Listens on UDP `:27950` and responds to `getservers <protocol> ...` with `getserversResponse` containing known servers (merged from every known master and direct heartbeats).
* Accepts `heartbeat` from game servers (adds/refreshes entry) and `shutdown` (removes entry).
* Heartbeat-sourced servers are polled immediately to enrich info and determine protocol.

---

## Web Viewer

The frontend is served at `/`. It includes:

* Auto-refresh every 10 seconds, with a sort order that stays stable across refreshes
* A shareable-by-URL filter bar: game (single-select), and version/mod (multi-select, with in-dropdown search and select-all/deselect-all)
* Q3-colorized player and bot lists, with suspected-bot flagging
* Per-server detail pages (`server.html`) with a 7-day player-count chart
* Network-wide player/server-count charts and an official-master uptime bar on the main page
* Click-to-copy IP
* 🟢/🔴 status indicators, with a broadcast icon and richer hover tooltips for servers sending direct heartbeats
* `masters.html` — status/uptime for every known master server
* `port-padding.html` — dashboard of detected same-IP port-padding groups, named for transparency
* `add-master.html` — per-game `sv_masterN` setup instructions for server owners
* `fix-ingame-browser.html` — hosts-file fix for players whose in-game browser is empty
* `ignored.html` — the curated block-list, named for transparency
* `contact.html` — email, GitHub, Discord, and Facebook links

---

## Project Structure

```
q3master/
├── cmd/
│   └── q3master/
│       └── main.go                # Application entrypoint (HTTP server wiring, env config)
├── internal/
│   ├── servers/                   # Master UDP, discovery, polling, clone detection, store
│   │   ├── q3master_poller.go     # Multi-master discovery (getservers)
│   │   ├── q3master_server.go     # Built-in UDP master (getservers/heartbeat responder)
│   │   ├── q3server_poller.go     # Per-server getstatus polling
│   │   ├── clones.go              # Duplicate/clone detection and collapsing
│   │   ├── playertrack.go         # Suspected-bot (spoofed real player) detection
│   │   ├── janitor.go             # Stale-entry eviction
│   │   ├── persistence.go         # State file load/save/seed
│   │   ├── types.go               # ServerEntry, known masters, curated block-list
│   │   └── util.go
│   ├── history/                   # Optional MongoDB-backed history (player/network/master uptime)
│   │   ├── history.go
│   │   ├── rollup.go              # Raw → hourly → daily rollups
│   │   ├── query.go
│   │   └── correction.go          # Read-time correction of network totals for known clone groups
│   └── httpapi/                   # HTTP handlers and middleware
│       ├── handlers.go
│       └── middleware.go
├── web/                            # Static frontend (Bootstrap 5, jQuery, no build step)
│   ├── index.html                  # Main server list
│   ├── server.html                 # Per-server detail page
│   ├── masters.html                 # Master server status/uptime
│   ├── port-padding.html            # Same-IP port-padding dashboard
│   ├── add-master.html              # Server-owner setup instructions
│   ├── fix-ingame-browser.html      # Player-facing hosts-file fix
│   ├── ignored.html                 # Curated block-list
│   ├── contact.html                 # Contact links
│   └── common.js                    # Shared rendering/filtering logic
├── go.mod / go.sum                 # Dependencies
└── Dockerfile                      # Build/run container image
```
