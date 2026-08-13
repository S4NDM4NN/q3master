package history

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// StartRollup periodically aggregates raw samples into hourly buckets, and
// hourly buckets into daily buckets, for both the per-server and
// network-wide collections. Each step is a $merge upsert on a unique key, so
// re-running over overlapping windows (including the still-in-progress
// current hour/day) is safe and just refreshes the bucket.
func StartRollup(interval time.Duration) {
	if !enabled {
		return
	}
	go func() {
		for {
			runRollup()
			time.Sleep(interval)
		}
	}()
}

func runRollup() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := rollup(ctx, samplesColl, "hourly", "address", "ts", "hour_ts", "hour"); err != nil {
		log.Printf("history: per-server hourly rollup failed: %v", err)
	}
	if err := rollup(ctx, hourlyColl, "daily", "address", "hour_ts", "day_ts", "day"); err != nil {
		log.Printf("history: per-server daily rollup failed: %v", err)
	}
	if err := rollupNetwork(ctx, networkSamplesColl, "network_hourly", "protocol", "ts", "total_players", "online_server_count", "hour_ts", "hour"); err != nil {
		log.Printf("history: network hourly rollup failed: %v", err)
	}
	if err := rollupNetwork(ctx, networkHourlyColl, "network_daily", "protocol", "hour_ts", "avg_players", "avg_online_servers", "day_ts", "day"); err != nil {
		log.Printf("history: network daily rollup failed: %v", err)
	}
	if err := rollupMaster(ctx, masterSamplesColl, "master_hourly", "ts", "hour_ts", "hour"); err != nil {
		log.Printf("history: master hourly rollup failed: %v", err)
	}
	if err := rollupMaster(ctx, masterHourlyColl, "master_daily", "hour_ts", "day_ts", "day"); err != nil {
		log.Printf("history: master daily rollup failed: %v", err)
	}
}

// rollup aggregates a per-server source collection (raw samples, keyed by
// player_count/max_players, or hourly buckets, keyed by avg/min/max_players)
// into a per-server destination collection grouped by metaField (address) +
// truncated time bucket, upserting via $merge.
func rollup(ctx context.Context, src *mongo.Collection, destName, metaField, srcTsField, destTsField, truncUnit string) error {
	playerField, minField, maxField, countExpr := perServerSourceFields(srcTsField)

	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: metaField, Value: "$" + metaField},
				{Key: destTsField, Value: bson.D{{Key: "$dateTrunc", Value: bson.D{
					{Key: "date", Value: "$" + srcTsField},
					{Key: "unit", Value: truncUnit},
				}}}},
			}},
			{Key: "avg_players", Value: bson.D{{Key: "$avg", Value: "$" + playerField}}},
			{Key: "min_players", Value: bson.D{{Key: "$min", Value: "$" + minField}}},
			{Key: "max_players", Value: bson.D{{Key: "$max", Value: "$" + maxField}}},
			{Key: "sample_count", Value: countExpr},
		}}},
		{{Key: "$project", Value: bson.D{
			{Key: "_id", Value: 0},
			{Key: metaField, Value: "$_id." + metaField},
			{Key: destTsField, Value: "$_id." + destTsField},
			{Key: "avg_players", Value: 1},
			{Key: "min_players", Value: 1},
			{Key: "max_players", Value: 1},
			{Key: "sample_count", Value: 1},
		}}},
		{{Key: "$merge", Value: bson.D{
			{Key: "into", Value: destName},
			{Key: "on", Value: bson.A{metaField, destTsField}},
			{Key: "whenMatched", Value: "replace"},
			{Key: "whenNotMatched", Value: "insert"},
		}}},
	}

	cur, err := src.Aggregate(ctx, pipeline)
	if err != nil {
		return err
	}
	return cur.Close(ctx)
}

// rollupNetwork is the network-wide equivalent of rollup: grouped by
// protocol bucket instead of server address, and the two source fields
// (total players / online server count) are given explicitly since the raw
// and hourly source collections name them differently
// (total_players/online_server_count vs avg_players/avg_online_servers).
func rollupNetwork(ctx context.Context, src *mongo.Collection, destName, metaField, srcTsField, playersField, onlineField, destTsField, truncUnit string) error {
	countExpr := sampleCountExpr(srcTsField)

	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: metaField, Value: "$" + metaField},
				{Key: destTsField, Value: bson.D{{Key: "$dateTrunc", Value: bson.D{
					{Key: "date", Value: "$" + srcTsField},
					{Key: "unit", Value: truncUnit},
				}}}},
			}},
			{Key: "avg_players", Value: bson.D{{Key: "$avg", Value: "$" + playersField}}},
			{Key: "min_players", Value: bson.D{{Key: "$min", Value: "$" + playersField}}},
			{Key: "max_players", Value: bson.D{{Key: "$max", Value: "$" + playersField}}},
			{Key: "avg_online_servers", Value: bson.D{{Key: "$avg", Value: "$" + onlineField}}},
			{Key: "min_online_servers", Value: bson.D{{Key: "$min", Value: "$" + onlineField}}},
			{Key: "max_online_servers", Value: bson.D{{Key: "$max", Value: "$" + onlineField}}},
			{Key: "sample_count", Value: countExpr},
		}}},
		{{Key: "$project", Value: bson.D{
			{Key: "_id", Value: 0},
			{Key: metaField, Value: "$_id." + metaField},
			{Key: destTsField, Value: "$_id." + destTsField},
			{Key: "avg_players", Value: 1},
			{Key: "min_players", Value: 1},
			{Key: "max_players", Value: 1},
			{Key: "avg_online_servers", Value: 1},
			{Key: "min_online_servers", Value: 1},
			{Key: "max_online_servers", Value: 1},
			{Key: "sample_count", Value: 1},
		}}},
		{{Key: "$merge", Value: bson.D{
			{Key: "into", Value: destName},
			{Key: "on", Value: bson.A{metaField, destTsField}},
			{Key: "whenMatched", Value: "replace"},
			{Key: "whenNotMatched", Value: "insert"},
		}}},
	}

	cur, err := src.Aggregate(ctx, pipeline)
	if err != nil {
		return err
	}
	return cur.Close(ctx)
}

// perServerSourceFields picks which fields to aggregate depending on whether
// the source collection is raw samples (srcTsField == "ts", uses
// player_count/max_players, counts documents) or an hourly rollup being
// rolled up again into daily (uses avg/min/max_players, sums sample_count).
func perServerSourceFields(srcTsField string) (playerField, minField, maxField string, countExpr bson.D) {
	if srcTsField == "ts" {
		return "player_count", "player_count", "max_players", sampleCountExpr(srcTsField)
	}
	return "avg_players", "min_players", "max_players", sampleCountExpr(srcTsField)
}

// sampleCountExpr is $sum:1 when aggregating raw samples (srcTsField == "ts")
// or $sum:"$sample_count" when re-aggregating an already-rolled-up
// collection, so counts accumulate correctly across both rollup stages.
func sampleCountExpr(srcTsField string) bson.D {
	if srcTsField == "ts" {
		return bson.D{{Key: "$sum", Value: 1}}
	}
	return bson.D{{Key: "$sum", Value: "$sample_count"}}
}

// rollupMaster aggregates the global master-status series (raw samples, or
// hourly buckets being rolled up again into daily) into destName, grouped
// only by truncated time bucket -- there's no meta field to partition by,
// since there's only one real master server being tracked.
func rollupMaster(ctx context.Context, src *mongo.Collection, destName, srcTsField, destTsField, truncUnit string) error {
	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateTrunc", Value: bson.D{
				{Key: "date", Value: "$" + srcTsField},
				{Key: "unit", Value: truncUnit},
			}}}},
			{Key: "uptime_pct", Value: masterUptimeExpr(srcTsField)},
			{Key: "sample_count", Value: sampleCountExpr(srcTsField)},
		}}},
		{{Key: "$project", Value: bson.D{
			{Key: "_id", Value: 0},
			{Key: destTsField, Value: "$_id"},
			{Key: "uptime_pct", Value: 1},
			{Key: "sample_count", Value: 1},
		}}},
		{{Key: "$merge", Value: bson.D{
			{Key: "into", Value: destName},
			{Key: "on", Value: destTsField},
			{Key: "whenMatched", Value: "replace"},
			{Key: "whenNotMatched", Value: "insert"},
		}}},
	}

	cur, err := src.Aggregate(ctx, pipeline)
	if err != nil {
		return err
	}
	return cur.Close(ctx)
}

// masterUptimeExpr picks how to compute uptime_pct (0-100) depending on
// whether the source is raw samples (srcTsField == "ts", boolean "up"
// field) or an hourly rollup being re-aggregated into daily (already a
// 0-100 uptime_pct field, simply re-averaged -- the same unweighted
// simplification the per-server/network daily rollups above already use).
//
// $group field expressions must themselves be accumulators (like $avg,
// $sum) -- $multiply is not one, so scaling to a 0-100 range has to happen
// *inside* the $avg's per-document expression (averaging 100/0 per
// document), not by wrapping the accumulator's result in $multiply
// afterward. The latter looks reasonable but fails at query time with
// "(Location40237) The $multiply accumulator is a unary operator" since
// Mongo interprets a bare $multiply in accumulator position as an attempt
// to use it as one.
func masterUptimeExpr(srcTsField string) bson.D {
	if srcTsField == "ts" {
		return bson.D{{Key: "$avg", Value: bson.D{{Key: "$cond", Value: bson.A{"$up", 100, 0}}}}}
	}
	return bson.D{{Key: "$avg", Value: "$uptime_pct"}}
}
