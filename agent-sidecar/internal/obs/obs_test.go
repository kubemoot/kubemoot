package obs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const parent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

func useRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return rec
}

func TestFieldsRoundTripTheTraceContext(t *testing.T) {
	useRecorder(t)
	ctx := FromFields(context.Background(), parent, "vendor=x")
	ctx, span := Tracer().Start(ctx, "child")
	defer span.End()
	tp, ts := Fields(ctx)
	if !strings.Contains(tp, "0af7651916cd43dd8448eb211c80319c") || ts != "vendor=x" {
		t.Fatalf("traceparent %q tracestate %q", tp, ts)
	}
	if got := FromFields(context.Background(), "", ""); got != context.Background() {
		t.Error("no fields, no change")
	}
	if tp, _ := Fields(context.Background()); tp != "" {
		t.Errorf("a context with no trace has no traceparent: %q", tp)
	}
}

func TestNATSHeadersCarryTheTraceContext(t *testing.T) {
	useRecorder(t)
	ctx, span := Tracer().Start(context.Background(), "publisher")
	defer span.End()
	m := &nats.Msg{}
	InjectNATS(ctx, m)
	if m.Header.Get(TraceparentHeader) == "" {
		t.Fatal("no header written")
	}
	got := ExtractNATS(context.Background(), m)
	if _, span2 := Tracer().Start(got, "consumer"); span2.SpanContext().TraceID() != span.SpanContext().TraceID() {
		t.Errorf("the consumer joined trace %s, want %s", span2.SpanContext().TraceID(), span.SpanContext().TraceID())
	}
	if ExtractNATS(context.Background(), &nats.Msg{}) != context.Background() {
		t.Error("a message with no headers changes nothing")
	}
}

func TestTransportSendsTheContextAndRecordsTheFailure(t *testing.T) {
	rec := useRecorder(t)
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	ctx, root := Tracer().Start(context.Background(), "root")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
	resp, err := (&http.Client{Transport: Transport(nil, "gateway")}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	root.End()
	if !strings.Contains(seen.Get(TraceparentHeader), root.SpanContext().TraceID().String()) {
		t.Errorf("the peer saw %q", seen.Get(TraceparentHeader))
	}
	var client *tracetest.SpanStub
	for _, s := range rec.Ended() {
		if s.Name() == "gateway GET" {
			st := tracetest.SpanStubFromReadOnlySpan(s)
			client = &st
		}
	}
	if client == nil || client.Status.Code != 1 { // codes.Error
		t.Errorf("a 502 marks the client span failed: %+v", client)
	}
}

func TestHandlerContinuesTheCallersTraceAndKeepsStreamingWorking(t *testing.T) {
	rec := useRecorder(t)
	flushed := make(chan struct{})
	srv := httptest.NewServer(Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("first"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush through the span writer: %v", err)
		}
		close(flushed)
	}), "door"))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1", nil)
	req.Header.Set(TraceparentHeader, parent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.Ended()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	spans := rec.Ended()
	if len(spans) == 0 || spans[0].SpanContext().TraceID().String() != "0af7651916cd43dd8448eb211c80319c" ||
		spans[0].Name() != "door GET /v1" {
		t.Fatalf("%v", spans)
	}
}

func TestMetricsUseAgentRuntimesNamesAndTheUniformLabels(t *testing.T) {
	m := New(Labels{Namespace: "n", Crew: "c", Agent: "a"}, "")
	m.Call(ModelCall{Server: "gpu1", Model: "m", Kind: "tool-turn", Cold: true, OK: true, Duration: 2 * time.Second, OutputTokens: 10})
	m.Call(ModelCall{Server: "gpu1", Model: "m", Kind: "simple", OK: false, Duration: time.Second})
	m.Waited("gpu1", "m", time.Second)
	m.Waited("", "m", time.Second)
	m.Turn("agree", time.Second)
	m.Signal("agree")
	m.ProgressAck()
	m.Delivery("turn")
	m.Door("model", "ok")
	for name, want := range map[string]int{
		"kubemoot_scheduler_calls_total": 2, "kubemoot_scheduler_call_seconds": 2, "kubemoot_scheduler_generated_tokens_total": 1,
		"kubemoot_scheduler_capacity_wait_seconds": 2, "kubemoot_sidecar_turns_total": 1, "kubemoot_sidecar_signals_total": 1,
		"kubemoot_sidecar_progress_acks_total": 1, "kubemoot_sidecar_deliveries_total": 1, "kubemoot_sidecar_door_requests_total": 1,
	} {
		if got, err := testutil.GatherAndCount(m.Registry, name); err != nil || got != want {
			t.Errorf("%s: %d series, want %d (%v)", name, got, want, err)
		}
	}
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), `kubemoot_scheduler_calls_total{agent="a",crew="c",kind="tool-turn",model="m",namespace="n",outcome="ok",policy="algorithm"`) {
		t.Errorf("labels and policy default: %s", buf[:n])
	}
}

func TestSetupWithoutACollectorStillPropagates(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{Attributes: map[string]string{"agent": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := Tracer().Start(context.Background(), "x")
	defer span.End()
	if tp, _ := Fields(ctx); tp == "" {
		t.Error("with no collector the context must still flow")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Error(err)
	}
}
