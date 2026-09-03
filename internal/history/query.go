package history

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Point is a single player-count history sample for a server, at whatever
// resolution (raw/hourly/daily) it was served from. PlayerCount is a float
// because hourly/daily points are averages.
type Point struct {
	Ts          time.Time `json:"ts"`
	PlayerCount float64   `json:"player_count"`
	MaxPlayers  int       `json:"max_players"`
}

// NetworkPoint is a single network-wide history sample.
type NetworkPoint struct {
	Ts                time.Time `json:"ts"`
	TotalPlayers      float64   `json:"total_players"`
	OnlineServerCount float64   `json:"online_server_count"`
}

// MasterPoint is a single history sample of the real (id Software) master
// server's reachability, at whatever resolution (raw/hourly/daily) it was
// served from. UptimePct is 0-100: for raw points it's simply 0 or 100
// (down/up for that single check); for hourly/daily points it's the
// fraction of checks in that bucket that succeeded.
type MasterPoint struct {
	Ts          time.Time `json:"ts"`
	UptimePct   float64   `json:"uptime_pct"`
	SampleCount int       `json:"sample_count"`
}

// ParseRange converts a range query param ("7d", "30d", "all") into a cutoff
// time. Unrecognized values fall back to "7d".
func ParseRange(rangeParam string) time.Time {
	now := time.Now().UTC()
	switch rangeParam {
	case "30d":
		return now.Add(-HourlyRetention)
	case "all":
		return time.Time{}
	default:
		return now.Add(-RawRetention)
	}
}

// GetServerHistory returns player-count history points for a server since
// the given cutoff, merging hourly/daily tiers depending on how far back the
// cutoff reaches -- never raw per-minute points, see the note above
// RawRetention in history.go.
func GetServerHistory(ctx context.Context, address string, since time.Time) ([]Point, error) {
	if !enabled {
		return []Point{}, nil
	}

	now := time.Now().UTC()
	hourlyCutoff := now.Add(-HourlyRetention)

	points := []Point{}

	if since.Before(hourlyCutoff) {
		docs, err := queryDaily(ctx, address, since, hourlyCutoff)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			points = append(points, Point{Ts: d.DayTs, PlayerCount: d.AvgPlayers, MaxPlayers: d.MaxPlayers})
		}
	}

	from := hourlyCutoff
	if since.After(hourlyCutoff) {
		from = since
	}
	docs, err := queryHourly(ctx, address, from, now)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		points = append(points, Point{Ts: d.HourTs, PlayerCount: d.AvgPlayers, MaxPlayers: d.MaxPlayers})
	}

	sort.Slice(points, func(i, j int) bool { return points[i].Ts.Before(points[j].Ts) })
	return points, nil
}

// GetNetworkHistory returns network-wide history points for one protocol
// bucket ("all" for the combined fleet, or a Q3 protocol number as a string
// e.g. "84") since the given cutoff, merging tiers the same way
// GetServerHistory does. aliases (address -> protocol, see
// servers.AliasAddresses) are addresses now known to be clone-detected
// duplicates of some other server; their own historical contribution is
// subtracted from the returned points (see correctForAliases) so charts
// recorded before a clone was known to be a duplicate read correctly. Pass
// nil/empty if the caller has none or doesn't care.
func GetNetworkHistory(ctx context.Context, protocol string, since time.Time, aliases map[string]int) ([]NetworkPoint, error) {
	if !enabled {
		return []NetworkPoint{}, nil
	}
	if protocol == "" {
		protocol = "all"
	}

	now := time.Now().UTC()
	hourlyCutoff := now.Add(-HourlyRetention)

	points := []NetworkPoint{}

	if since.Before(hourlyCutoff) {
		docs, err := queryNetworkDaily(ctx, protocol, since, hourlyCutoff)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			points = append(points, NetworkPoint{Ts: d.DayTs, TotalPlayers: d.AvgPlayers, OnlineServerCount: d.AvgOnlineServers})
		}
	}

	from := hourlyCutoff
	if since.After(hourlyCutoff) {
		from = since
	}
	docs, err := queryNetworkHourly(ctx, protocol, from, now)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		points = append(points, NetworkPoint{Ts: d.HourTs, TotalPlayers: d.AvgPlayers, OnlineServerCount: d.AvgOnlineServers})
	}

	sort.Slice(points, func(i, j int) bool { return points[i].Ts.Before(points[j].Ts) })
	points = correctForAliases(ctx, points, protocol, aliases, since)
	return points, nil
}

// GetMasterDailyUptime returns one uptime point per calendar day for the
// trailing `days` days (including today, which keeps updating as the day's
// rollup re-runs) for the given master host, always at daily granularity.
// Unlike GetServerHistory/GetNetworkHistory's tiered raw/hourly/daily merge
// (built for a continuous line chart that wants finer resolution near
// "now"), this powers a status-page-style day-by-day uptime bar, where
// every bar must represent exactly one calendar day regardless of how
// recent it is.
func GetMasterDailyUptime(ctx context.Context, host string, days int) ([]MasterPoint, error) {
	if !enabled {
		return []MasterPoint{}, nil
	}
	if days <= 0 {
		days = 90
	}

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -days)

	docs, err := queryMasterHostDaily(ctx, host, from, now)
	if err != nil {
		return nil, err
	}
	points := make([]MasterPoint, 0, len(docs))
	for _, d := range docs {
		points = append(points, MasterPoint{Ts: d.DayTs, UptimePct: d.UptimePct, SampleCount: d.SampleCount})
	}
	return points, nil
}

func queryMasterHostDaily(ctx context.Context, host string, from, to time.Time) ([]masterHostDailyDoc, error) {
	filter := bson.D{
		{Key: "host", Value: host},
		{Key: "day_ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	cur, err := masterHostDailyColl.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "day_ts", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []masterHostDailyDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func queryRaw(ctx context.Context, address string, from, to time.Time) ([]sampleDoc, error) {
	filter := bson.D{
		{Key: "address", Value: address},
		{Key: "ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	cur, err := samplesColl.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "ts", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []sampleDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func queryHourly(ctx context.Context, address string, from, to time.Time) ([]hourlyDoc, error) {
	filter := bson.D{
		{Key: "address", Value: address},
		{Key: "hour_ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	cur, err := hourlyColl.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "hour_ts", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []hourlyDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func queryDaily(ctx context.Context, address string, from, to time.Time) ([]dailyDoc, error) {
	filter := bson.D{
		{Key: "address", Value: address},
		{Key: "day_ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	cur, err := dailyColl.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "day_ts", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []dailyDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func queryNetworkHourly(ctx context.Context, protocol string, from, to time.Time) ([]networkHourlyDoc, error) {
	filter := bson.D{
		{Key: "protocol", Value: protocol},
		{Key: "hour_ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	cur, err := networkHourlyColl.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "hour_ts", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []networkHourlyDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func queryNetworkDaily(ctx context.Context, protocol string, from, to time.Time) ([]networkDailyDoc, error) {
	filter := bson.D{
		{Key: "protocol", Value: protocol},
		{Key: "day_ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	cur, err := networkDailyColl.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "day_ts", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []networkDailyDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}
