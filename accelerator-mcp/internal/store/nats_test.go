package store

import (
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// natsKV starts an embedded JetStream server with the two buckets and returns the KV over it.
func natsKV(t *testing.T) NATS {
	t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true,
		StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server did not start")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{TicketsBucket, PerformanceBucket} {
		if _, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatal(err)
		}
	}
	return NATS{JS: js}
}

func TestNATS_getPutListDelete(t *testing.T) {
	kv := natsKV(t)

	missing, err := kv.GetKVValue(TicketsBucket, "gpu-a__none")
	check(t, "a missing key is nil", err == nil && missing == nil, err)

	check(t, "put", kv.PutKVValue(TicketsBucket, "gpu-a__1", []byte(`{"a":1}`)) == nil, nil)
	check(t, "put", kv.PutKVValue(TicketsBucket, "gpu-b__2", []byte(`{"b":2}`)) == nil, nil)
	value, err := kv.GetKVValue(TicketsBucket, "gpu-a__1")
	check(t, "get", err == nil && string(value) == `{"a":1}`, string(value))

	keys, err := kv.ListKVKeys(TicketsBucket)
	check(t, "list from the subject index", err == nil && len(keys) == 2 && keys[0] == "gpu-a__1", keys)

	check(t, "delete", kv.DeleteKVKey(TicketsBucket, "gpu-a__1") == nil, nil)
	gone, err := kv.GetKVValue(TicketsBucket, "gpu-a__1")
	check(t, "a deleted key reads as nil", err == nil && gone == nil, err)
}

func TestNATS_ticketsOverARealBucket(t *testing.T) {
	kv := natsKV(t)
	tickets := Tickets{KV: kv, Holder: testHolder, Now: fixedNow}
	key, err := tickets.Claim(testProvider, testModel, 512)
	check(t, "claim", err == nil, err)
	_ = kv.PutKVValue(TicketsBucket, testProvider+"__crew", []byte(`{"holderId":"crew/agent","modelName":"qwen3:8b"}`))

	others, err := tickets.Others(testProvider)
	check(t, "others", err == nil && others == 1, others)
	tickets.Release(key)
	tickets.Release(testProvider + "__crew")
	others, err = tickets.Others(testProvider)
	check(t, "a deleted ticket no longer counts", err == nil && others == 0, others)
}

func TestNATS_aMissingBucketIsAnError(t *testing.T) {
	kv := natsKV(t)
	_, err := kv.GetKVValue("kubemoot_no_such_bucket", "k")
	check(t, "get", err != nil, err)
	_, err = kv.ListKVKeys("kubemoot_no_such_bucket")
	check(t, "list", err != nil, err)
	check(t, "put", kv.PutKVValue("kubemoot_no_such_bucket", "k", nil) != nil, nil)
	check(t, "delete", kv.DeleteKVKey("kubemoot_no_such_bucket", "k") != nil, nil)
}
