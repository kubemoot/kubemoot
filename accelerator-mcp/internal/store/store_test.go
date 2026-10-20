package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/storetest"
)

const (
	testServer = "gpu-a"
	testModel  = "qwen3:14b"
	testHolder = "kubemoot/accelerator-mcp/pod-a"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC) }

func check(t *testing.T, what string, ok bool, got any) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: %+v", what, got)
	}
}

func otherTicket(kv *storetest.MemKV) {
	_ = kv.PutKVValue(TicketsBucket, testServer+"__crew-1",
		[]byte(`{"holderId":"crew-ns/agent","server":"gpu-a","modelName":"qwen3:8b","modelFootprintMiB":512}`))
}

func historyOf(t *testing.T, kv *storetest.MemKV) bus.SchedulerPerformance {
	t.Helper()
	raw, _ := kv.GetKVValue(PerformanceBucket, bus.PerformanceKey(testServer, testModel, testHolder))
	var entry bus.SchedulerPerformance
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("history entry: %v", err)
	}
	return entry
}

func TestTickets_othersCountsOnlyOtherHoldersOnThisServer(t *testing.T) {
	kv := storetest.NewMemKV()
	tickets := Tickets{KV: kv, Holder: testHolder, Now: fixedNow}
	_, err := tickets.Claim(testServer, testModel, 512)
	check(t, "claim", err == nil, err)
	_ = kv.PutKVValue(TicketsBucket, "gpu-b__x", []byte(`{"holderId":"crew/agent"}`))
	_ = kv.PutKVValue(TicketsBucket, "residency__gpu-a__qwen3_3a14b", []byte(`{"holderId":"crew/agent"}`))

	n, err := tickets.Others(testServer)
	check(t, "own, other-server, and residency entries are not others", err == nil && n == 0, n)
	otherTicket(kv)
	_ = kv.PutKVValue(TicketsBucket, "gpu-a__garbled", []byte(`not json`))
	n, _ = tickets.Others(testServer)
	check(t, "a crew ticket counts, and so does one that cannot be parsed", n == 2, n)
}

func TestTickets_aReadErrorIsAnError_notAnIdleServer(t *testing.T) {
	kv := storetest.NewMemKV()
	tickets := Tickets{KV: kv, Holder: testHolder, Now: fixedNow}
	otherTicket(kv)
	kv.FailGet = true
	_, err := tickets.Others(testServer)
	check(t, "get fails", errors.Is(err, storetest.ErrKV), err)
	_, err = tickets.ModelsInUse(testServer)
	check(t, "models in use fails too", errors.Is(err, storetest.ErrKV), err)
	kv.FailGet, kv.FailList = false, true
	_, err = tickets.Others(testServer)
	check(t, "list fails", errors.Is(err, storetest.ErrKV), err)
}

func TestTickets_modelsInUseNamesOnlyOtherHolders(t *testing.T) {
	kv := storetest.NewMemKV()
	tickets := Tickets{KV: kv, Holder: testHolder, Now: fixedNow}
	_, _ = tickets.Claim(testServer, testModel, 512)
	otherTicket(kv)
	inUse, err := tickets.ModelsInUse(testServer)
	check(t, "models in use", err == nil && len(inUse) == 1 && inUse["qwen3:8b"], inUse)
}

func TestTickets_claimWritesTheBusTicket_andReleaseDeletesIt(t *testing.T) {
	kv := storetest.NewMemKV()
	tickets := Tickets{KV: kv, Holder: testHolder, Now: fixedNow}
	key, err := tickets.Claim(testServer, testModel, 9_000)
	check(t, "claim", err == nil && strings.HasPrefix(key, testServer+"__"), key)
	raw, _ := kv.GetKVValue(TicketsBucket, key)
	var ticket bus.ServerTicket
	check(t, "parses as the bus type", json.Unmarshal(raw, &ticket) == nil, string(raw))
	check(t, "fields", ticket.Server == testServer && ticket.ModelName == testModel &&
		ticket.ModelFootprintMiB == 9_000 && ticket.HolderID == testHolder && ticket.ClaimedAt != "", ticket)
	tickets.Release(key)
	check(t, "released", len(kv.Keys(TicketsBucket)) == 0, kv.Keys(TicketsBucket))

	kv.FailPut = true
	_, err = tickets.Claim(testServer, testModel, 1)
	check(t, "a failed write is an error", err != nil, err)
}

func TestHistory_appendsKeepsTheLatest_andForgetsOneServer(t *testing.T) {
	kv := storetest.NewMemKV()
	h := History{KV: kv, Agent: testHolder, Now: fixedNow}
	check(t, "no samples, no write", h.Record(testServer, testModel, nil) == nil &&
		len(kv.Keys(PerformanceBucket)) == 0, nil)
	for i := 0; i < MaxSamples+4; i++ {
		_ = h.Record(testServer, testModel, []Sample{{At: int64(i), Kind: KindCalibration, Ok: i%2 == 0}})
	}
	_ = h.Record("gpu-b", testModel, []Sample{{Kind: KindCalibration, Ok: true}})

	entry := historyOf(t, kv)
	check(t, "bounded history", len(entry.Samples) == MaxSamples && entry.Samples[0].At == 4 &&
		entry.Requests == MaxSamples+4 && entry.Failures == (MaxSamples+4)/2 && entry.Waits != nil, entry)
	check(t, "forget", h.Forget(testServer) == nil, nil)
	keys := kv.Keys(PerformanceBucket)
	check(t, "only the server's history is forgotten", len(keys) == 1 && strings.HasPrefix(keys[0], "gpu-b."), keys)
}

func TestHistory_aReadErrorNeverReplacesTheStoredHistory(t *testing.T) {
	kv := storetest.NewMemKV()
	h := History{KV: kv, Agent: testHolder, Now: fixedNow}
	_ = h.Record(testServer, testModel, []Sample{{Kind: KindCalibration, Ok: true}})
	kv.FailGet = true
	err := h.Record(testServer, testModel, []Sample{{Kind: KindCalibration, Ok: true}})
	check(t, "error", errors.Is(err, storetest.ErrKV), err)
	kv.FailGet = false
	check(t, "kept", historyOf(t, kv).Requests == 1, historyOf(t, kv))
}

func TestHistory_aCorruptEntryIsRebuilt(t *testing.T) {
	kv := storetest.NewMemKV()
	h := History{KV: kv, Agent: testHolder, Now: fixedNow}
	_ = kv.PutKVValue(PerformanceBucket, bus.PerformanceKey(testServer, testModel, testHolder), []byte(`{"samples":3`))
	check(t, "record", h.Record(testServer, testModel, []Sample{{Kind: KindCalibration, Ok: true}}) == nil, nil)
	check(t, "rebuilt", historyOf(t, kv).Requests == 1, historyOf(t, kv))
}

func TestHistory_forgetReportsAFailedDelete(t *testing.T) {
	kv := storetest.NewMemKV()
	h := History{KV: kv, Agent: testHolder, Now: fixedNow}
	_ = h.Record(testServer, testModel, []Sample{{Kind: KindCalibration, Ok: true}})
	kv.FailDelete = true
	check(t, "delete error", errors.Is(h.Forget(testServer), storetest.ErrKV), nil)
}
