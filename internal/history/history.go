// Package history records player-count samples for servers and the overall
// tracked fleet, and serves them back downsampled into hourly/daily
// aggregates as they age. It is a no-op (Enabled() == false) whenever
// MONGO_URI isn't configured, so callers never need to guard call sites.
package history

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Retention windows. Hourly/daily TTLs are measured from the bucket's own
// timestamp (via a Mongo TTL index), so each gives a rolling window of that
// length from now - not "on top of" the raw retention below. Raw and hourly
// windows overlap (both cover the most recent week); query.go picks whichever
// tier best matches the requested range.
const (
	RawRetention    = 7 * 24 * time.Hour
	HourlyRetention = 30 * 24 * time.Hour
)

var (
	db *mongo.Database

	samplesColl        *mongo.Collection
	hourlyColl         *mongo.Collection
	dailyColl          *mongo.Collection
	networkSamplesColl *mongo.Collection
	networkHourlyColl  *mongo.Collection
	networkDailyColl   *mongo.Collection

	enabled bool

	sampleCh        chan sampleDoc
	networkSampleCh chan networkSampleDoc
)

// Enabled reports whether history tracking is active.
func Enabled() bool {
	return enabled
}

// Init connects to MongoDB and ensures collections/indexes exist. If uri is
// empty, history tracking is left disabled.
func Init(uri, dbName string) error {
	if uri == "" {
		log.Println("history: MONGO_URI not set, player-count history tracking disabled")
		return nil
	}
	if dbName == "" {
		dbName = "q3master"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return fmt.Errorf("connect to mongo: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return fmt.Errorf("ping mongo: %w", err)
	}

	db = client.Database(dbName)

	if err := ensureCollections(ctx); err != nil {
		return fmt.Errorf("ensure collections: %w", err)
	}

	samplesColl = db.Collection("samples")
	hourlyColl = db.Collection("hourly")
	dailyColl = db.Collection("daily")
	networkSamplesColl = db.Collection("network_samples")
	networkHourlyColl = db.Collection("network_hourly")
	networkDailyColl = db.Collection("network_daily")

	sampleCh = make(chan sampleDoc, 1024)
	networkSampleCh = make(chan networkSampleDoc, 64)
	for i := 0; i < 2; i++ {
		go sampleWriter()
	}
	go networkSampleWriter()

	enabled = true
	log.Println("history: connected to MongoDB, player-count history tracking enabled")
	return nil
}

func ensureCollections(ctx context.Context) error {
	if err := createTimeSeries(ctx, "samples", "address", RawRetention); err != nil {
		return err
	}
	if err := createTimeSeries(ctx, "network_samples", "protocol", RawRetention); err != nil {
		return err
	}
	if err := createTieredIndexes(ctx, "hourly", "address", "hour_ts", HourlyRetention); err != nil {
		return err
	}
	if err := createTieredIndexes(ctx, "daily", "address", "day_ts", 0); err != nil {
		return err
	}
	if err := createTieredIndexes(ctx, "network_hourly", "protocol", "hour_ts", HourlyRetention); err != nil {
		return err
	}
	if err := createTieredIndexes(ctx, "network_daily", "protocol", "day_ts", 0); err != nil {
		return err
	}
	return nil
}

func createTimeSeries(ctx context.Context, name, metaField string, expireAfter time.Duration) error {
	tsOpts := options.TimeSeries().SetTimeField("ts").SetGranularity("seconds")
	if metaField != "" {
		tsOpts = tsOpts.SetMetaField(metaField)
	}
	opts := options.CreateCollection().
		SetTimeSeriesOptions(tsOpts).
		SetExpireAfterSeconds(int64(expireAfter.Seconds()))

	err := db.CreateCollection(ctx, name, opts)
	if err == nil || isNamespaceExists(err) {
		return nil
	}
	return err
}

// createTieredIndexes sets up a rollup collection partitioned by metaField
// (address for per-server collections, protocol for network-wide ones): a
// unique compound index for upsert targeting, plus (if ttl > 0) a separate
// TTL index on the timestamp field alone (TTL indexes must be single-field).
func createTieredIndexes(ctx context.Context, collName, metaField, tsField string, ttl time.Duration) error {
	coll := db.Collection(collName)
	models := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: metaField, Value: 1}, {Key: tsField, Value: 1}},
			Options: options.Index().SetUnique(true),
		},
	}
	if ttl > 0 {
		models = append(models, mongo.IndexModel{
			Keys:    bson.D{{Key: tsField, Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(ttl.Seconds())),
		})
	}
	_, err := coll.Indexes().CreateMany(ctx, models)
	return err
}

func isNamespaceExists(err error) bool {
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		return ce.Code == 48
	}
	return false
}

// --- document shapes ---

type sampleDoc struct {
	Ts          time.Time `bson:"ts"`
	Address     string    `bson:"address"`
	PlayerCount int       `bson:"player_count"`
	MaxPlayers  int       `bson:"max_players"`
	Map         string    `bson:"map"`
}

type hourlyDoc struct {
	Address     string    `bson:"address"`
	HourTs      time.Time `bson:"hour_ts"`
	AvgPlayers  float64   `bson:"avg_players"`
	MinPlayers  int       `bson:"min_players"`
	MaxPlayers  int       `bson:"max_players"`
	SampleCount int       `bson:"sample_count"`
}

type dailyDoc struct {
	Address     string    `bson:"address"`
	DayTs       time.Time `bson:"day_ts"`
	AvgPlayers  float64   `bson:"avg_players"`
	MinPlayers  int       `bson:"min_players"`
	MaxPlayers  int       `bson:"max_players"`
	SampleCount int       `bson:"sample_count"`
}

// networkSampleDoc/networkHourlyDoc/networkDailyDoc are partitioned by
// Protocol so the network-wide overview can be filtered to a single game
// protocol, not just the "all" aggregate. Protocol holds a Q3 protocol
// number as a string (e.g. "84"), or "all" for the combined-fleet bucket.
type networkSampleDoc struct {
	Ts                time.Time `bson:"ts"`
	Protocol          string    `bson:"protocol"`
	TotalPlayers      int       `bson:"total_players"`
	OnlineServerCount int       `bson:"online_server_count"`
}

type networkHourlyDoc struct {
	Protocol         string    `bson:"protocol"`
	HourTs           time.Time `bson:"hour_ts"`
	AvgPlayers       float64   `bson:"avg_players"`
	MinPlayers       int       `bson:"min_players"`
	MaxPlayers       int       `bson:"max_players"`
	AvgOnlineServers float64   `bson:"avg_online_servers"`
	MinOnlineServers int       `bson:"min_online_servers"`
	MaxOnlineServers int       `bson:"max_online_servers"`
	SampleCount      int       `bson:"sample_count"`
}

type networkDailyDoc struct {
	Protocol         string    `bson:"protocol"`
	DayTs            time.Time `bson:"day_ts"`
	AvgPlayers       float64   `bson:"avg_players"`
	MinPlayers       int       `bson:"min_players"`
	MaxPlayers       int       `bson:"max_players"`
	AvgOnlineServers float64   `bson:"avg_online_servers"`
	MinOnlineServers int       `bson:"min_online_servers"`
	MaxOnlineServers int       `bson:"max_online_servers"`
	SampleCount      int       `bson:"sample_count"`
}

// --- recording ---

// RecordSample records a single successful poll's player count for a server.
// Non-blocking: if history tracking is disabled or the write queue is full,
// the sample is dropped rather than slowing down polling.
func RecordSample(address string, playerCount, maxPlayers int, mapName string) {
	if !enabled {
		return
	}
	doc := sampleDoc{
		Ts:          time.Now().UTC(),
		Address:     address,
		PlayerCount: playerCount,
		MaxPlayers:  maxPlayers,
		Map:         mapName,
	}
	select {
	case sampleCh <- doc:
	default:
		log.Println("history: sample write queue full, dropping sample")
	}
}

func sampleWriter() {
	for doc := range sampleCh {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := samplesColl.InsertOne(ctx, doc); err != nil {
			log.Printf("history: failed to record sample for %s: %v", doc.Address, err)
		}
		cancel()
	}
}

// RecordNetworkSample records a single network-wide snapshot: total real
// players and online server count for one protocol bucket ("all" for the
// combined fleet, or a Q3 protocol number as a string e.g. "84" for just
// that protocol), so the overview can later be filtered per-protocol.
func RecordNetworkSample(protocol string, totalPlayers, onlineServers int) {
	if !enabled {
		return
	}
	doc := networkSampleDoc{
		Ts:                time.Now().UTC(),
		Protocol:          protocol,
		TotalPlayers:      totalPlayers,
		OnlineServerCount: onlineServers,
	}
	select {
	case networkSampleCh <- doc:
	default:
		log.Println("history: network sample write queue full, dropping sample")
	}
}

func networkSampleWriter() {
	for doc := range networkSampleCh {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := networkSamplesColl.InsertOne(ctx, doc); err != nil {
			log.Printf("history: failed to record network sample: %v", err)
		}
		cancel()
	}
}
