package a2a

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
)

const keySignal = "signal"

type harness struct {
	url      string
	canceled chan struct{}
	mu       sync.Mutex
	seen     http.Header
	got      *sdk.Message
}

type events func(ec *a2asrv.ExecutorContext) []sdk.Event

// serve runs an official-SDK A2A server whose executor yields what script returns.
func serve(t *testing.T, streaming bool, ext []sdk.AgentExtension, script events) *harness {
	t.Helper()
	h := &harness{canceled: make(chan struct{}, 1)}
	exec := a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[sdk.Event, error] {
		h.mu.Lock()
		h.got = ec.Message
		h.mu.Unlock()
		return func(yield func(sdk.Event, error) bool) {
			for _, e := range script(ec) {
				if !yield(e, nil) {
					return
				}
				if _, wait := e.(*sdk.TaskStatusUpdateEvent); wait && ec.Message != nil && strings.Contains(ec.Message.Parts[0].Text(), "hang") {
					<-ctx.Done()
					return
				}
			}
		}
	})
	srv := httptest.NewServer(nil)
	t.Cleanup(srv.Close)
	card := &sdk.AgentCard{
		Name:                "test-harness",
		SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface(srv.URL, sdk.TransportProtocolHTTPJSON)},
		Capabilities:        sdk.AgentCapabilities{Streaming: streaming, Extensions: ext},
		DefaultInputModes:   []string{"text"}, DefaultOutputModes: []string{"text"},
	}
	handler := a2asrv.NewHandler(&cancelRecorder{AgentExecutor: exec, canceled: h.canceled})
	mux := http.NewServeMux()
	mux.Handle("/", a2asrv.NewRESTHandler(handler))
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = r.Header.Clone()
		h.mu.Unlock()
		mux.ServeHTTP(w, r)
	})
	h.url = srv.URL
	return h
}

type cancelRecorder struct {
	a2asrv.AgentExecutor
	canceled chan struct{}
}

func (c *cancelRecorder) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[sdk.Event, error] {
	select {
	case c.canceled <- struct{}{}:
	default:
	}
	return c.AgentExecutor.Cancel(ctx, ec)
}

func replyText(text string) events {
	return func(*a2asrv.ExecutorContext) []sdk.Event {
		return []sdk.Event{sdk.NewMessage(sdk.MessageRoleAgent, sdk.NewTextPart(text))}
	}
}

func turn(question string) *sdk.Message {
	return NewTurn("thread-1", question, TurnInput{Kind: "discussion", Agent: "a", Crew: "c", Phase: "EVALUATING", TurnKey: "k"})
}

func discover(t *testing.T, h *harness) *Client {
	t.Helper()
	c, err := Discover(context.Background(), h.url, &http.Client{Transport: obs.Transport(nil, "harness")})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestABlockingHarnessThatAnswersWithAMessage(t *testing.T) {
	h := serve(t, false, nil, replyText("two nodes"))
	r, err := discover(t, h).Do(context.Background(), turn("which nodes?"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := Interpret(r, nil); o.Signal != SignalAgree || o.Content != "two nodes" {
		t.Fatalf("%+v", o)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.got.ContextID != "thread-1" || h.got.Parts[0].Text() != "which nodes?" {
		t.Errorf("the harness must get the thread as the A2A context and the question as text: %+v", h.got)
	}
	data, _ := h.got.Parts[1].Data().(map[string]any)
	if data["profile"] != ProfileURI || data["turnKey"] != "k" {
		t.Errorf("the turn profile must ride in the data part: %v", data)
	}
}

func taskScript(final sdk.TaskState, parts ...*sdk.Part) events {
	return func(ec *a2asrv.ExecutorContext) []sdk.Event {
		out := []sdk.Event{sdk.NewSubmittedTask(ec, ec.Message), sdk.NewStatusUpdateEvent(ec, sdk.TaskStateWorking, nil)}
		if len(parts) > 0 {
			out = append(out, sdk.NewArtifactEvent(ec, parts...))
		}
		return append(out, sdk.NewStatusUpdateEvent(ec, final, nil))
	}
}

func TestAStreamingHarnessIsReducedToOneResultAndTheTaskIdArrivesEarly(t *testing.T) {
	data := map[string]any{keySignal: "concern", "content": "the node count is stale", "reason": "cache",
		"evidence": []any{map[string]any{"tool": "kubernetes_resources_list", "note": "labels"}}}
	h := serve(t, true, nil, taskScript(sdk.TaskStateCompleted, sdk.NewDataPart(data)))
	var ids []sdk.TaskID
	r, err := discover(t, h).Do(context.Background(), turn("q"), func(id sdk.TaskID) { ids = append(ids, id) })
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Errorf("the task id must be announced once: %v", ids)
	}
	o := Interpret(r, nil)
	if o.Signal != SignalConcern || o.Content != "the node count is stale" || o.Reason != "cache" ||
		len(o.Evidence) != 1 || o.Evidence[0].Tool != "kubernetes_resources_list" {
		t.Fatalf("%+v", o)
	}
}

func TestABlockingHarnessThatAnswersWithATask(t *testing.T) {
	h := serve(t, false, nil, taskScript(sdk.TaskStateCompleted, sdk.NewTextPart("plain answer")))
	r, err := discover(t, h).Do(context.Background(), turn("q"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := Interpret(r, nil); o.Signal != SignalAgree || o.Content != "plain answer" {
		t.Fatalf("%+v", o)
	}
}

func TestTheTurnCanBeCanceledOnceTheTaskIsKnown(t *testing.T) {
	h := serve(t, true, nil, taskScript(sdk.TaskStateCompleted, sdk.NewTextPart("never")))
	c := discover(t, h)
	ctx, stop := context.WithCancel(context.Background())
	id := make(chan sdk.TaskID, 1)
	done := make(chan error, 1)
	go func() {
		_, err := c.Do(ctx, turn("hang please"), func(i sdk.TaskID) { id <- i })
		done <- err
	}()
	var task sdk.TaskID
	select {
	case task = <-id:
	case <-time.After(5 * time.Second):
		t.Fatal("the task id never arrived")
	}
	if err := c.Cancel(context.Background(), task); err != nil {
		t.Logf("cancel answered %v (a harness may refuse)", err)
	}
	select {
	case <-h.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the harness was not told to cancel")
	}
	stop()
	<-done
}

func TestAHarnessOnAnotherProfileIsAMismatch(t *testing.T) {
	h := serve(t, false, []sdk.AgentExtension{{URI: "https://kubemoot.org/a2a/turn/v2"}}, replyText("x"))
	_, err := Discover(context.Background(), h.url, http.DefaultClient)
	if !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("%v", err)
	}
	ok := serve(t, false, []sdk.AgentExtension{{URI: ProfileURI}, {URI: "https://example.com/other"}}, replyText("x"))
	if _, err := Discover(context.Background(), ok.url, http.DefaultClient); err != nil {
		t.Fatalf("a harness that declares v1 is served: %v", err)
	}
}

func TestACardWithoutAnHTTPJSONInterfaceIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"grpc-only","supportedInterfaces":[{"url":"x:1","protocolBinding":"GRPC","protocolVersion":"1.0"}],` +
			`"capabilities":{},"defaultInputModes":["text"],"defaultOutputModes":["text"]}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := Discover(context.Background(), srv.URL, http.DefaultClient); err == nil {
		t.Fatal("a harness the sidecar cannot speak to is refused at discovery")
	}
}

func TestAliveFollowsTheCard(t *testing.T) {
	h := serve(t, false, nil, replyText("x"))
	if !Alive(context.Background(), h.url, http.DefaultClient) {
		t.Fatal("a serving harness is alive")
	}
	if Alive(context.Background(), "http://127.0.0.1:1", http.DefaultClient) {
		t.Fatal("nothing listens there")
	}
}

func TestTheTraceContextReachesTheHarness(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	h := serve(t, false, nil, replyText("x"))
	c := discover(t, h)
	ctx, span := obs.Tracer().Start(context.Background(), "turn")
	defer span.End()
	if _, err := c.Do(ctx, turn("q"), nil); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !strings.Contains(h.seen.Get(obs.TraceparentHeader), span.SpanContext().TraceID().String()) {
		t.Fatalf("the harness saw traceparent %q, want trace %s", h.seen.Get(obs.TraceparentHeader), span.SpanContext().TraceID())
	}
}

func TestInterpretTable(t *testing.T) {
	text := func(s string) []*sdk.Part { return []*sdk.Part{sdk.NewTextPart(s)} }
	data := func(m map[string]any) []*sdk.Part { return []*sdk.Part{sdk.NewDataPart(m)} }
	cases := []struct {
		name         string
		r            Result
		err          error
		signal, kind string
	}{
		{"plain text is agree", Result{State: sdk.TaskStateCompleted, Parts: text("hi")}, nil, SignalAgree, ""},
		{"the final message when no artifact", Result{State: sdk.TaskStateCompleted, Message: sdk.NewMessage(sdk.MessageRoleAgent, text("m")...)}, nil, SignalAgree, ""},
		{"structured block", Result{State: sdk.TaskStateCompleted, Parts: data(map[string]any{keySignal: "block", "content": "no"})}, nil, SignalBlock, ""},
		{"structured stand aside needs no content", Result{State: sdk.TaskStateCompleted, Parts: data(map[string]any{keySignal: "stand_aside", "reason": "not mine"})}, nil, SignalStandAside, ""},
		{"structured agree with no content is a failure", Result{State: sdk.TaskStateCompleted, Parts: data(map[string]any{keySignal: "agree"})}, nil, SignalFailure, FailureEmpty},
		{"an unknown signal falls back to the text", Result{State: sdk.TaskStateCompleted, Parts: append(data(map[string]any{keySignal: "shrug"}), text("fine")...)}, nil, SignalAgree, ""},
		{"empty text is a failure, never silence", Result{State: sdk.TaskStateCompleted, Parts: text("  ")}, nil, SignalFailure, FailureEmpty},
		{"no parts at all", Result{State: sdk.TaskStateCompleted}, nil, SignalFailure, FailureEmpty},
		{"rejected is a stand aside", Result{State: sdk.TaskStateRejected}, nil, SignalStandAside, ""},
		{"input required", Result{State: sdk.TaskStateInputRequired}, nil, SignalFailure, "input_required"},
		{"auth required", Result{State: sdk.TaskStateAuthRequired}, nil, SignalFailure, "input_required"},
		{"canceled", Result{State: sdk.TaskStateCanceled}, nil, SignalFailure, "harness_canceled"},
		{"failed", Result{State: sdk.TaskStateFailed}, nil, SignalFailure, "harness_failed"},
		{"a stream that ended early", Result{State: sdk.TaskStateWorking}, nil, SignalFailure, "harness_failed"},
		{"a transport error", Result{}, errors.New("refused"), SignalFailure, "harness_error"},
	}
	for _, c := range cases {
		o := Interpret(c.r, c.err)
		if o.Signal != c.signal || o.FailureType != c.kind {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, o.Signal, o.FailureType, c.signal, c.kind)
		}
	}
}
