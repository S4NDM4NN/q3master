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
// the given cutoff, merging raw/hourly/daily tiers depending on how far back
// the cutoff reaches.
func GetServerHistory(ctx context.Context, address string, since time.Time) ([]Point, error) {
	if !enabled {
		return []Point{}, nil
	}

	now := time.Now().UTC()
	rawCutoff := now.Add(-RawRetention)
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

	if since.Before(rawCutoff) {
		from := hourlyCutoff
		if since.After(hourlyCutoff) {
			from = since
		}
		docs, err := queryHourly(ctx, address, from, rawCutoff)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			points = append(points, Point{Ts: d.HourTs, PlayerCount: d.AvgPlayers, MaxPlayers: d.MaxPlayers})
		}
	}

	from := rawCutoff
	if since.After(rawCutoff) {
		from = since
	}
	rawDocs, err := queryRaw(ctx, address, from, now)
	if err != nil {
		return nil, err
	}
	for _, d := range rawDocs {
		points = append(points, Point{Ts: d.Ts, PlayerCount: float64(d.PlayerCount), MaxPlayers: d.MaxPlayers})
	}

	sort.Slice(points, func(i, j int) bool { return points[i].Ts.Before(points[j].Ts) })
	return points, nil
}

// GetNetworkHistory returns network-wide history points since the given
// cutoff, merging tiers the same way GetServerHistory does.
func GetNetworkHistory(ctx context.Context, since time.Time) ([]NetworkPoint, error) {
	if !enabled {
		return []NetworkPoint{}, nil
	}

	now := time.Now().UTC()
	rawCutoff := now.Add(-RawRetention)
	hourlyCutoff := now.Add(-HourlyRetention)

	points := []NetworkPoint{}

	if since.Before(hourlyCutoff) {
		docs, err := queryNetworkDaily(ctx, since, hourlyCutoff)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			points = append(points, NetworkPoint{Ts: d.DayTs, TotalPlayers: d.AvgPlayers, OnlineServerCount: d.AvgOnlineServers})
		}
	}

	if since.Before(rawCutoff) {
		from := hourlyCutoff
		if since.After(hourlyCutoff) {
			from = since
		}
		docs, err := queryNetworkHourly(ctx, from, rawCutoff)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			points = append(points, NetworkPoint{Ts: d.HourTs, TotalPlayers: d.AvgPlayers, OnlineServerCount: d.AvgOnlineServers})
		}
	}

	from := rawCutoff
	if since.After(rawCutoff) {
		from = since
	}
	rawDocs, err := queryNetworkRaw(ctx, from, now)
	if err != nil {
		return nil, err
	}
	for _, d := range rawDocs {
		points = append(points, NetworkPoint{Ts: d.Ts, TotalPlayers: float64(d.TotalPlayers), OnlineServerCount: float64(d.OnlineServerCount)})
	}

	sort.Slice(points, func(i, j int) bool { return points[i].Ts.Before(points[j].Ts) })
	return points, nil
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

func queryNetworkRaw(ctx context.Context, from, to time.Time) ([]networkSampleDoc, error) {
	filter := bson.D{{Key: "ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}}}
	cur, err := networkSamplesColl.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "ts", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []networkSampleDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func queryNetworkHourly(ctx context.Context, from, to time.Time) ([]networkHourlyDoc, error) {
	filter := bson.D{{Key: "hour_ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}}}
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

func queryNetworkDaily(ctx context.Context, from, to time.Time) ([]networkDailyDoc, error) {
	filter := bson.D{{Key: "day_ts", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}}}
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
