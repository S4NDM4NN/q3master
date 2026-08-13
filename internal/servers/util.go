package servers

import (
    "strconv"
    "strings"
)

func parseInt(s string) int {
    n, err := strconv.Atoi(s)
    if err != nil {
        return 0
    }
    return n
}

// parseMatchTime parses a Q3A-family "Score_Time" value ("M:SS" or
// "H:MM:SS", as baseq3-derived mods report elapsed time in the current
// match) into total seconds. ET/RTCW don't send an equivalent field, so
// this only ever succeeds for Q3A/OpenArena-style servers.
func parseMatchTime(raw string) (int, bool) {
    parts := strings.Split(raw, ":")
    if len(parts) < 2 || len(parts) > 3 {
        return 0, false
    }
    secs := 0
    for _, p := range parts {
        n, err := strconv.Atoi(p)
        if err != nil {
            return 0, false
        }
        secs = secs*60 + n
    }
    return secs, true
}

