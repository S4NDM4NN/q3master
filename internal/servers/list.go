package servers

// ListServers returns a snapshot slice of all servers.
func ListServers() []*ServerEntry {
    serverMutex.Lock()
    defer serverMutex.Unlock()

    list := make([]*ServerEntry, 0, len(serverList))
    for _, s := range serverList {
        list = append(list, s)
    }
    return list
}

