package modeldoor

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const (
	completions = `{"model":"default","messages":[]}`
	traceID     = "0af7651916cd43dd8448eb211c80319c"
)

func TestTheThreadAndAliasReachThePlacementService(t *testing.T) {
	var seen map[string]any
	door, placer, reg := newDoor(t, jsonUpstream(&seen), nil)
	reg.Open("k1", turn.Info{ThreadID: threadNine})
	_ = post(t, door.URL, "k1", `{"model":"triage","messages":[]}`).Body.Close()
	got := placer.askedFirst()
	if got.ThreadID != threadNine || got.Alias != aliasTriage || got.Agent != "a" || got.Crew != "c" {
		t.Fatalf("%+v", got)
	}
}

func TestAWaitingCallTellsTheTurnsOwner(t *testing.T) {
	var seen map[string]any
	door, placer, reg := newDoor(t, jsonUpstream(&seen), nil)
	placer.queued = placement.KindGPUBusy
	var heard []string
	reg.Open("k", turn.Info{OnWaiting: func(r string) { heard = append(heard, r) }})
	_ = post(t, door.URL, "k", completions).Body.Close()
	if !slices.Equal(heard, []string{placement.KindGPUBusy}) {
		t.Fatalf("heard %v", heard)
	}
}

func TestADegradedCallIsMarkedAndNeedsNoRelease(t *testing.T) {
	var seen map[string]any
	door, placer, reg := newDoor(t, jsonUpstream(&seen), nil)
	placer.degraded = true
	var heard []string
	meter := reg.Open("k", turn.Info{OnWaiting: func(r string) { heard = append(heard, r) }})
	_ = post(t, door.URL, "k", completions).Body.Close()
	if !slices.Equal(heard, []string{"placement-unavailable"}) {
		t.Errorf("a degraded turn must say so on the bus: %v", heard)
	}
	// The reply is complete before the door records the call, so the record is waited for.
	var got any
	for deadline := time.Now().Add(5 * time.Second); got == nil && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		got = meter.Snapshot()["pickReason"]
	}
	if got != "degraded: warm" {
		t.Errorf("pickReason %v", got)
	}
	if len(placer.releasedTickets()) != 0 {
		t.Error("a degraded call holds no ticket")
	}
}

func TestNoFitRecordsItsKindOnTheTurn(t *testing.T) {
	door, _, reg := newDoor(t, jsonUpstream(new(map[string]any)),
		&placement.ErrNoFit{Kind: placement.KindModelTooLarge, Reason: "x"})
	meter := reg.Open("k", turn.Info{})
	_ = post(t, door.URL, "k", completions).Body.Close()
	if meter.NoFitKind() != placement.KindModelTooLarge {
		t.Fatalf("kind %q", meter.NoFitKind())
	}
}

func TestReleaseReportsWhatTheCallDid(t *testing.T) {
	var seen map[string]any
	door, placer, _ := newDoor(t, jsonUpstream(&seen), nil)
	_ = post(t, door.URL, "k", completions).Body.Close()
	placer.waitReleased(t)
	placer.mu.Lock()
	defer placer.mu.Unlock()
	r := placer.reports[0]
	if !r.OK || r.PromptTokens != 7 || r.CompletionTokens != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestAFailingServerFailsTheCallButStillReleases(t *testing.T) {
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) })
	door, placer, _ := newDoor(t, up, nil)
	resp := post(t, door.URL, "k", completions)
	_ = resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("the server's status passes through: %d", resp.StatusCode)
	}
	placer.waitReleased(t)
	placer.mu.Lock()
	defer placer.mu.Unlock()
	if placer.reports[0].OK {
		t.Error("a 500 is not a good call")
	}
}

func TestTheModelCallsAreCountedUnderAgentRuntimesNames(t *testing.T) {
	var seen map[string]any
	up := httptest.NewServer(jsonUpstream(&seen))
	t.Cleanup(up.Close)
	m := obs.New(obs.Labels{Namespace: "n", Crew: "c", Agent: "a"}, "algorithm")
	placer := &fakePlacer{endpoint: up.URL}
	door := httptest.NewServer(New(Config{Crew: "c", Agent: "a", Aliases: map[string][]string{aliasDefault: {"m"}}},
		placer, turn.NewRegistry(), m))
	t.Cleanup(door.Close)
	_ = post(t, door.URL, "k", `{"model":"default","tools":[{"type":"function"}],"messages":[]}`).Body.Close()
	placer.waitReleased(t)
	n, err := testutil.GatherAndCount(m.Registry, "kubemoot_scheduler_calls_total", "kubemoot_scheduler_call_seconds",
		"kubemoot_scheduler_generated_tokens_total")
	if err != nil || n != 3 {
		t.Fatalf("want one series of each of the three scheduler metrics, got %d (%v)", n, err)
	}
}

func TestTheTraceContinuesFromTheHarnessToTheModelServer(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	var upstreamTrace string
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamTrace = r.Header.Get(obs.TraceparentHeader)
		_, _ = w.Write([]byte(`{}`))
	})
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	door := httptest.NewServer(obs.Handler(New(Config{Crew: "c", Agent: "a",
		Aliases: map[string][]string{aliasDefault: {"m"}}}, &fakePlacer{endpoint: upstream.URL}, turn.NewRegistry(), nil), "model-door"))
	t.Cleanup(door.Close)

	req, _ := http.NewRequest(http.MethodPost, door.URL+"/v1/chat/completions", strings.NewReader(completions))
	req.Header.Set(obs.TraceparentHeader, "00-"+traceID+"-b7ad6b7169203331-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(upstreamTrace, traceID) {
		t.Fatalf("the model server must see the harness's trace id, got %q", upstreamTrace)
	}
	if strings.Contains(upstreamTrace, "b7ad6b7169203331") {
		t.Errorf("the model server's parent must be the sidecar's span, not the harness's: %q", upstreamTrace)
	}
	var names []string
	for deadline := time.Now().Add(5 * time.Second); len(names) < 2 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond) // the server span ends just after the client has its reply
		names = names[:0]
		for _, s := range rec.Ended() {
			if s.SpanContext().TraceID().String() == traceID {
				names = append(names, s.Name())
			}
		}
	}
	if len(names) < 2 {
		t.Errorf("want a server span and a client span in the harness's trace, got %v", names)
	}
}
