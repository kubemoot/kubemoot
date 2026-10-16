package probe

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/store"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/storetest"
)

const (
	provider = "gpu-a"
	model    = "qwen3:14b"
	holder   = "kubemoot/accelerator-mcp/pod-a"
)

var t0 = time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newProber(kv *storetest.MemKV, eng *storetest.Engine, c *clock) *Prober {
	return &Prober{
		Engine:  func(string) engine.Engine { return eng },
		Tickets: store.Tickets{KV: kv, Holder: holder, Now: c.Now},
		History: store.History{KV: kv, Agent: holder, Now: c.Now},
		Prompts: DefaultPrompts(),
		Now:     c.Now,
	}
}

func request(kind string) Request {
	return Request{Provider: provider, Endpoint: "http://gpu-a", Model: model, Kind: kind, MaxParallel: 2,
		SizeMiB: 9_000, MemoryMiB: 20_000, Accelerator: true}
}

func crewTicket(kv *storetest.MemKV, m string) {
	_ = kv.PutKVValue(store.TicketsBucket, provider+"__crew-1",
		[]byte(`{"holderId":"crew-ns/agent","provider":"gpu-a","modelName":"`+m+`"}`))
}

func check(t *testing.T, what string, ok bool, got any) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: %+v", what, got)
	}
}

func TestLoad_measuresTheColdLoad_andRemembersTheModel(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	eng.Sizes[model] = 9_000
	p := newProber(kv, eng, &clock{t0})

	r, err := p.Probe(context.Background(), request(KindLoad))
	check(t, "no error", err == nil, err)
	check(t, "load", r.Fits && r.LoadMs == 3000 && !r.Busy && r.Resident != nil && r.Resident.VRAMMiB == 9_000, r)
	check(t, "tickets released", len(kv.Keys(store.TicketsBucket)) == 0, kv.Keys(store.TicketsBucket))
	check(t, "remembered for Finish", len(p.loaded[provider]) == 1, p.loaded)
}

func TestShortLongAndParallel_reportTheirSpeeds(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	eng.Resident[model] = engine.Resident{Model: model, SizeMiB: 9_000, VRAMMiB: 9_000}
	p := newProber(kv, eng, &clock{t0})

	short, _ := p.Probe(context.Background(), request(KindShort))
	check(t, "short", short.GenerateTokensPerSecond == 50 && short.LoadMs == 0, short)
	long, _ := p.Probe(context.Background(), request(KindLong))
	check(t, "long", long.PromptTokensPerSecond > 9_900 && long.PromptTokensPerSecond < 10_100, long)
	par, _ := p.Probe(context.Background(), request(KindParallel))
	check(t, "parallel", par.ParallelRequests == 2 && par.ParallelGenerateTokensPerSecond == 100, par)
	check(t, "a resident model is not ours to unload", len(p.loaded) == 0, p.loaded)
}

func TestProbes_recordCalibrationSamples(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	p := newProber(kv, eng, &clock{t0})
	_, _ = p.Probe(context.Background(), request(KindLoad))
	_, _ = p.Probe(context.Background(), request(KindParallel))

	raw, _ := kv.GetKVValue(store.PerformanceBucket, store.Token(provider)+"."+store.Token(model)+"."+store.Token(holder))
	var entry bus.SchedulerPerformance
	_ = json.Unmarshal(raw, &entry)
	check(t, "three samples", len(entry.Samples) == 3, entry.Samples)
	check(t, "the first is cold and every one is calibration",
		entry.Samples[0].ColdStart && !entry.Samples[1].ColdStart && entry.Samples[2].Kind == store.KindCalibration &&
			entry.Samples[2].ActiveCalls == 2, entry.Samples)
}

func TestSpill_onAnAccelerator_isUnfit_butNotOnACPU(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	eng.Sizes[model] = 9_000
	eng.VRAMFraction = 0.7
	p := newProber(kv, eng, &clock{t0})

	r, _ := p.Probe(context.Background(), request(KindLoad))
	check(t, "spill", !r.Fits && r.Reason != "" && r.Resident.VRAMMiB == 6_300, r)

	cpu := request(KindShort)
	cpu.Accelerator = false
	r, _ = p.Probe(context.Background(), cpu)
	check(t, "no spill check on a CPU provider", r.Fits && r.Reason == "", r)
}

func TestRefusals(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	p := newProber(kv, eng, &clock{t0})

	big := request(KindLoad)
	big.SizeMiB = 25_000
	r, err := p.Probe(context.Background(), big)
	check(t, "too large: nothing sent", err == nil && !r.Fits && eng.Generates == 0, r)

	_, err = p.Probe(context.Background(), request("warm-up"))
	check(t, "unknown probe", errors.Is(err, ErrUnknownProbe), err)

	crewTicket(kv, model)
	r, err = p.Probe(context.Background(), request(KindShort))
	check(t, "busy: nothing sent", err == nil && r.Busy && eng.Generates == 0, r)
}

func TestACrewCallArrivingDuringAProbe_marksTheResultBusy(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	eng.OnGenerate = func(int) { crewTicket(kv, "qwen3:8b") }
	p := newProber(kv, eng, &clock{t0})

	r, err := p.Probe(context.Background(), request(KindShort))
	check(t, "measured, then busy", err == nil && r.Busy && r.GenerateTokensPerSecond == 50, r)
	check(t, "only the crew ticket remains", len(kv.Keys(store.TicketsBucket)) == 1, kv.Keys(store.TicketsBucket))
}

func TestAFailingEngine_isAnError_andReleasesTickets(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	eng.Fail = true
	p := newProber(kv, eng, &clock{t0})

	_, err := p.Probe(context.Background(), request(KindParallel))
	check(t, "error", errors.Is(err, storetest.ErrEngine), err)
	check(t, "tickets released", len(kv.Keys(store.TicketsBucket)) == 0, kv.Keys(store.TicketsBucket))

	raw, _ := kv.GetKVValue(store.PerformanceBucket, store.Token(provider)+"."+store.Token(model)+"."+store.Token(holder))
	var entry bus.SchedulerPerformance
	_ = json.Unmarshal(raw, &entry)
	check(t, "failed requests are recorded without engine timings",
		entry.Failures == 2 && entry.Samples[0].LoadMs == store.Unknown && entry.Samples[0].EvalMs == store.Unknown, entry)
	check(t, "a model it tried to load is remembered for Finish", len(p.loaded[provider]) == 1, p.loaded)
}

func TestFinish_unloadsWhatItLoaded_exceptAModelACrewNowUses(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	p := newProber(kv, eng, &clock{t0})
	_, _ = p.Probe(context.Background(), request(KindLoad))
	other := request(KindLoad)
	other.Model = "qwen3:8b"
	_, _ = p.Probe(context.Background(), other)

	crewTicket(kv, "qwen3:8b")
	unloaded, err := p.Finish(context.Background(), provider)
	check(t, "unloads only the unused one", err == nil && len(unloaded) == 1 && unloaded[0] == model, unloaded)
	check(t, "keeps the one in use for later", len(p.loaded[provider]) == 1, p.loaded)

	_ = kv.DeleteKVKey(store.TicketsBucket, provider+"__crew-1")
	unloaded, _ = p.Finish(context.Background(), provider)
	check(t, "a later Finish unloads it", len(unloaded) == 1 && len(p.loaded) == 0, unloaded)
}

func TestFinish_aTicketReadErrorUnloadsNothing_andAFailedUnloadIsKept(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	p := newProber(kv, eng, &clock{t0})
	_, _ = p.Probe(context.Background(), request(KindLoad))

	kv.FailGet = true
	crewTicket(kv, "qwen3:8b")
	_, err := p.Finish(context.Background(), provider)
	check(t, "read error", errors.Is(err, storetest.ErrKV) && len(eng.Unloads) == 0, err)

	kv.FailGet = false
	_ = kv.DeleteKVKey(store.TicketsBucket, provider+"__crew-1")
	eng.FailUnload = true
	unloaded, err := p.Finish(context.Background(), provider)
	check(t, "unload failed", errors.Is(err, storetest.ErrEngine) && len(unloaded) == 0 &&
		len(p.loaded[provider]) == 1, unloaded)
}

func TestFinishIdle_unloadsAfterTheIdleTime(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	c := &clock{t0}
	p := newProber(kv, eng, c)
	_, _ = p.Probe(context.Background(), request(KindLoad))

	c.now = t0.Add(5 * time.Minute)
	p.FinishIdle(context.Background(), 10*time.Minute)
	check(t, "not yet idle", len(eng.Unloads) == 0, eng.Unloads)

	c.now = t0.Add(9 * time.Minute)
	_, _ = p.Probe(context.Background(), request(KindShort))
	c.now = t0.Add(15 * time.Minute)
	p.FinishIdle(context.Background(), 10*time.Minute)
	check(t, "a later probe of the model refreshes its idle clock", len(eng.Unloads) == 0, eng.Unloads)

	c.now = t0.Add(20 * time.Minute)
	p.FinishIdle(context.Background(), 10*time.Minute)
	check(t, "idle: unloaded", len(eng.Unloads) == 1, eng.Unloads)
}

func TestACrewThatClaimsBetweenTheCheckAndTheClaim_stopsTheProbe(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	p := newProber(kv, eng, &clock{t0})
	racing := &racingKV{MemKV: kv}
	p.Tickets.KV = racing

	r, err := p.Probe(context.Background(), request(KindShort))
	check(t, "busy, nothing sent", err == nil && r.Busy && eng.Generates == 0, r)
	check(t, "our ticket released", len(kv.Keys(store.TicketsBucket)) == 1, kv.Keys(store.TicketsBucket))
	check(t, "a model it never loaded is not remembered", len(p.loaded[provider]) == 0, p.loaded)
}

// blockingEngine holds Generate until release is closed.
type blockingEngine struct {
	*storetest.Engine
	started chan struct{}
	release chan struct{}
}

func (b *blockingEngine) Generate(ctx context.Context, m, prompt string, n int) (engine.Reply, error) {
	close(b.started)
	<-b.release
	return b.Engine.Generate(ctx, m, prompt, n)
}

func TestFinish_waitsForAProbeInFlight(t *testing.T) {
	kv, inner := storetest.NewMemKV(), storetest.NewEngine()
	eng := &blockingEngine{Engine: inner, started: make(chan struct{}), release: make(chan struct{})}
	p := newProber(kv, inner, &clock{t0})
	p.Engine = func(string) engine.Engine { return eng }

	probed := make(chan struct{})
	go func() {
		_, _ = p.Probe(context.Background(), request(KindLoad))
		close(probed)
	}()
	<-eng.started
	finished := make(chan []string)
	go func() {
		unloaded, _ := p.Finish(context.Background(), provider)
		finished <- unloaded
	}()
	select {
	case <-finished:
		t.Fatal("Finish ran while the probe was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(eng.release)
	<-probed
	unloaded := <-finished
	check(t, "then it unloads the probed model", len(unloaded) == 1 && unloaded[0] == model, unloaded)
}

// racingKV adds a crew ticket the moment the probe writes its own.
type racingKV struct{ *storetest.MemKV }

func (r *racingKV) PutKVValue(bucket, key string, value []byte) error {
	if err := r.MemKV.PutKVValue(bucket, key, value); err != nil {
		return err
	}
	if bucket == store.TicketsBucket {
		crewTicket(r.MemKV, "qwen3:8b")
	}
	return nil
}

func TestAFailedClaim_isAnError_andSendsNothing(t *testing.T) {
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	kv.FailPut = true
	p := newProber(kv, eng, &clock{t0})
	_, err := p.Probe(context.Background(), request(KindShort))
	check(t, "claim error", errors.Is(err, storetest.ErrKV) && eng.Generates == 0, err)
}

func TestPerSecond(t *testing.T) {
	check(t, "perSecond", perSecond(100, 2000) == 50 && perSecond(0, 5) == 0 && perSecond(5, 0) == 0, nil)
}
