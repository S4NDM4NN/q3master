package servers

import (
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "path/filepath"
    "time"
)

// SaveState writes a snapshot of the current server list to path as JSON.
func SaveState(path string) error {
    serverMutex.Lock()
    snapshot := make(map[string]*ServerEntry, len(serverList))
    for addr, s := range serverList {
        entryCopy := *s
        snapshot[addr] = &entryCopy
    }
    serverMutex.Unlock()

    data, err := json.MarshalIndent(snapshot, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal server state: %w", err)
    }

    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return fmt.Errorf("ensure state dir: %w", err)
    }

    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil {
        return fmt.Errorf("write temp state file: %w", err)
    }
    if err := os.Rename(tmp, path); err != nil {
        return fmt.Errorf("replace state file: %w", err)
    }
    return nil
}

// LoadState loads a previously saved server list from path, merging it into
// the in-memory list. It is not an error for the file to be missing.
func LoadState(path string) error {
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return fmt.Errorf("read state file: %w", err)
    }

    var loaded map[string]*ServerEntry
    if err := json.Unmarshal(data, &loaded); err != nil {
        return fmt.Errorf("unmarshal state file: %w", err)
    }

    serverMutex.Lock()
    for addr, entry := range loaded {
        serverList[addr] = entry
    }
    serverMutex.Unlock()
    return nil
}

// LoadOrSeed loads persisted state from path if present. Otherwise, as a
// one-time migration, it seeds the list from seedURL (the old standalone
// instance's /api/servers) and immediately persists it, so this only ever
// happens once per state file.
func LoadOrSeed(path, seedURL string) error {
    if _, err := os.Stat(path); err == nil {
        return LoadState(path)
    } else if !os.IsNotExist(err) {
        return fmt.Errorf("stat state file: %w", err)
    }

    if seedURL == "" {
        return nil
    }

    seeded, err := fetchSeed(seedURL)
    if err != nil {
        fmt.Printf("seed fetch from %s failed, starting empty: %v\n", seedURL, err)
        return nil
    }

    now := time.Now()
    serverMutex.Lock()
    for _, e := range seeded {
        if e.Address == "" {
            continue
        }
        e.State = StateNew
        e.Online = false
        e.MissedPolls = 0
        e.LastAttempt = time.Time{}
        e.LastGoodPoll = time.Time{}
        e.FirstSeen = now
        e.LastSeen = now
        serverList[e.Address] = e
    }
    serverMutex.Unlock()

    return SaveState(path)
}

func fetchSeed(seedURL string) ([]*ServerEntry, error) {
    client := http.Client{Timeout: 10 * time.Second}
    resp, err := client.Get(seedURL)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, seedURL)
    }

    var entries []*ServerEntry
    if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
        return nil, fmt.Errorf("decode seed response: %w", err)
    }
    return entries, nil
}

// StartAutosave periodically persists the server list to path.
func StartAutosave(path string, interval time.Duration) {
    ticker := time.NewTicker(interval)
    go func() {
        for range ticker.C {
            if err := SaveState(path); err != nil {
                fmt.Printf("autosave failed: %v\n", err)
            }
        }
    }()
}
