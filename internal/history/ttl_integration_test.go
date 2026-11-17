package history

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// TestHourlyTTLIsExtendedInPlace needs a throwaway MongoDB (Q3_TEST_MONGO_URI, e.g. mongodb://localhost:27017): a database
// created by an older build has the hourly TTL index at 30 days, and Init must move it to HourlyRetention without losing rows.
func TestHourlyTTLIsExtendedInPlace(t *testing.T) {
	uri := os.Getenv("Q3_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("Q3_TEST_MONGO_URI not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	dbName := "q3master_ttl_test"
	defer client.Database(dbName).Drop(context.Background())
	hourly := client.Database(dbName).Collection("hourly")

	// The old layout: unique index plus a 30 day TTL on hour_ts.
	if _, err := hourly.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "address", Value: 1}, {Key: "hour_ts", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "hour_ts", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(30 * 24 * 3600)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := hourly.InsertOne(ctx, bson.D{{Key: "address", Value: "1.2.3.4:27960"}, {Key: "hour_ts", Value: time.Now().Add(-40 * 24 * time.Hour)}}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ { // the second Init is the restart: nothing to change, no error
		if err := Init(uri, dbName); err != nil {
			t.Fatalf("Init #%d: %v", i+1, err)
		}
	}

	cur, err := hourly.Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var idx []bson.M
	if err := cur.All(ctx, &idx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ix := range idx {
		if ix["name"] == "hour_ts_1" {
			found = true
			var got int64
			switch v := ix["expireAfterSeconds"].(type) {
			case int32:
				got = int64(v)
			case int64:
				got = v
			case float64:
				got = int64(v)
			}
			if want := int64(HourlyRetention.Seconds()); got != want {
				t.Errorf("hour_ts TTL = %ds, want %ds", got, want)
			}
		}
	}
	if !found {
		t.Fatal("no hour_ts_1 index after Init")
	}
	if n, _ := hourly.CountDocuments(ctx, bson.D{}); n != 1 {
		t.Errorf("rows after Init = %d, want 1", n)
	}
}
