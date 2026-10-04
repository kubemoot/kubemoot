package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
)

// jetStreamServer runs an in-process NATS server with the discussion and request
// streams, and returns a gateway client connected to it.
func jetStreamServer(t *testing.T) (*natsclient.Client, jetstream.JetStream) {
	t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server did not start")
	}
	t.Cleanup(srv.Shutdown)

	nc := natsclient.NewClient(srv.ClientURL())
	if err := nc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, subject := range map[string]string{streamName: "kubemoot.discuss.>", "KUBEMOOT_REQUEST": "kubemoot.request.>"} {
		if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: name, Subjects: []string{subject}}); err != nil {
			t.Fatal(err)
		}
	}
	return nc, js
}

// publishDiscuss publishes one discussion message of crew "pilot" in namespace "ns"
// and returns its stream sequence.
func publishDiscuss(t *testing.T, js jetstream.JetStream, msg map[string]interface{}) uint64 {
	t.Helper()
	data, _ := json.Marshal(msg)
	ack, err := js.Publish(context.Background(), "kubemoot.discuss.ns.pilot.broadcast."+msg["threadId"].(string), data)
	if err != nil {
		t.Fatal(err)
	}
	return ack.Sequence
}

// startOf is the thread_start of thread for the test conversation.
func startOf(thread string) map[string]interface{} {
	return map[string]interface{}{
		"messageId": "start-" + thread, "messageType": msgThreadStart, "threadId": thread,
		"metadata": map[string]string{"conversationId": testConv},
	}
}

func msgOf(thread, id, messageType, content string) map[string]interface{} {
	return map[string]interface{}{"messageId": id, "messageType": messageType, "threadId": thread, "agentName": testAgent, "content": content}
}

// readStream reads a stream to its end and returns its non-empty field lines.
func readStream(t *testing.T, srv *httptest.Server, path string, header http.Header) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("stream did not end on thread_close: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(body), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func gatewayServer(t *testing.T, nc *natsclient.Client) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(nc, "ns").RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// The 2026-09-30 rollout: the first coordinator started thread A and was replaced; its
// successor answered the same request under thread B. The stream follows B to done.
func TestStreamFollowsTheThreadACoordinatorRestartStarted(t *testing.T) {
	nc, js := jetStreamServer(t)
	since := time.Now().UTC().Format(time.RFC3339Nano)
	publishDiscuss(t, js, startOf("A"))
	publishDiscuss(t, js, msgOf("A", "a1", "triaging", ""))
	b := publishDiscuss(t, js, startOf("B"))
	publishDiscuss(t, js, msgOf("B", "b1", msgSynthesis, "the namespaces are ..."))
	closeSeq := publishDiscuss(t, js, msgOf("B", "b2", "thread_close", ""))

	lines := readStream(t, gatewayServer(t, nc), "/api/v1/discussions/pilot/conv-1/stream?since="+url.QueryEscape(since), nil)
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		`data: {"type":"thread_found","threadId":"A"}`,
		`id: B:` + itoa(b) + "\n" + `data: {"type":"thread_found","threadId":"B"}`,
		`data: {"type":"synthesis","content":"the namespaces are ..."}`,
		`id: B:` + itoa(closeSeq) + "\n" + `data: {"type":"done"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stream lacks %q:\n%s", want, got)
		}
	}
}

// A client that lost its connection sends the last id it saw and receives only what
// came after, including the answer, without the events it already has.
func TestStreamResumesAfterTheLastEventID(t *testing.T) {
	nc, js := jetStreamServer(t)
	publishDiscuss(t, js, startOf("A"))
	publishDiscuss(t, js, msgOf("A", "a1", sigConcern, "seen before the drop"))
	seen := publishDiscuss(t, js, msgOf("A", "a2", sigAgree, "also seen"))
	publishDiscuss(t, js, msgOf("A", "a3", msgSynthesis, "answer"))
	publishDiscuss(t, js, msgOf("A", "a4", "thread_close", ""))
	srv := gatewayServer(t, nc)

	for name, path := range map[string]string{
		"header": "/api/v1/discussions/pilot/conv-1/stream",
		"query":  "/api/v1/discussions/pilot/conv-1/stream?lastEventId=" + url.QueryEscape(eventID("A", seen)),
	} {
		header := http.Header{}
		if name == "header" {
			header.Set("Last-Event-ID", eventID("A", seen))
		}
		got := strings.Join(readStream(t, srv, path, header), "\n")
		if strings.Contains(got, "seen before the drop") || strings.Contains(got, "also seen") || strings.Contains(got, "thread_found") {
			t.Errorf("%s: resumed stream repeated events:\n%s", name, got)
		}
		if !strings.Contains(got, `"content":"answer"`) || !strings.Contains(got, `"type":"done"`) {
			t.Errorf("%s: resumed stream lacks the answer:\n%s", name, got)
		}
	}
}

// A dual-published message is stored twice under one messageId. A client whose last
// event was the first copy must not receive the second after it resumes.
func TestStreamResumeSkipsTheTwinOfTheLastDeliveredMessage(t *testing.T) {
	nc, js := jetStreamServer(t)
	publishDiscuss(t, js, startOf("A"))
	first := publishDiscuss(t, js, msgOf("A", "dual", sigConcern, "said once"))
	publishDiscuss(t, js, msgOf("A", "dual", sigConcern, "said once"))
	publishDiscuss(t, js, msgOf("A", "a9", "thread_close", ""))
	header := http.Header{}
	header.Set("Last-Event-ID", eventID("A", first))
	got := strings.Join(readStream(t, gatewayServer(t, nc), "/api/v1/discussions/pilot/conv-1/stream", header), "\n")
	if strings.Contains(got, "said once") {
		t.Fatalf("the twin of the last delivered message was sent again:\n%s", got)
	}
	if !strings.Contains(got, `"type":"done"`) {
		t.Fatalf("want done:\n%s", got)
	}
}

// At shutdown open streams end at once, so their clients reconnect elsewhere, while a
// question posted meanwhile is still queued.
func TestEndStreamsOnEndsStreamsButNotQuestions(t *testing.T) {
	nc, js := jetStreamServer(t)
	publishDiscuss(t, js, startOf("A"))
	shutdown, stop := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	NewHandler(nc, "ns").EndStreamsOn(shutdown).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	time.AfterFunc(200*time.Millisecond, stop)
	got := strings.Join(readStream(t, srv, "/api/v1/discussions/pilot/conv-1/stream", nil), "\n")
	if !strings.Contains(got, "thread_found") || strings.Contains(got, `"type":"done"`) {
		t.Fatalf("want the stream to end open after thread_found:\n%s", got)
	}
	resp, err := http.Post(srv.URL+"/api/v1/discussions/pilot", mimeJSON, strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a question during shutdown: status %d, want 200", resp.StatusCode)
	}
}

func TestStreamRejectsAMalformedResumePointOrSince(t *testing.T) {
	nc, _ := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	for _, path := range []string{
		"/api/v1/discussions/pilot/conv-1/stream?lastEventId=garbage",
		"/api/v1/discussions/pilot/conv-1/stream?since=yesterday",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, resp.StatusCode)
		}
	}
}

// The POST returns when it queued the request, for the client to send back as since.
func TestPostDiscussionReturnsRequestedAt(t *testing.T) {
	nc, _ := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	before := time.Now()
	resp, err := http.Post(srv.URL+"/api/v1/discussions/pilot", mimeJSON, strings.NewReader(`{"message":"hi","conversationId":"conv-9"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body postDiscussionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, body.RequestedAt)
	if err != nil || body.ConversationID != "conv-9" || at.Before(before.Add(-time.Second)) {
		t.Fatalf("got %+v (%v)", body, err)
	}
}

func TestReadyFollowsTheNATSConnection(t *testing.T) {
	nc, _ := jetStreamServer(t)
	if code := readyStatus(t, nc); code != http.StatusOK {
		t.Fatalf("connected: status %d, want 200", code)
	}
	down := natsclient.NewClient("nats://127.0.0.1:1")
	if err := down.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(down.Close)
	if code := readyStatus(t, down); code != http.StatusServiceUnavailable {
		t.Fatalf("not connected: status %d, want 503", code)
	}
	if code := readyStatus(t, natsclient.NewClient("")); code != http.StatusServiceUnavailable {
		t.Fatalf("not configured: status %d, want 503", code)
	}
}

// A request the gateway cannot queue ends its stream with an explanation, not silence.
func TestCoordinatorErrorEndsTheStreamWithAnAnswer(t *testing.T) {
	nc, _ := jetStreamServer(t)
	h := NewHandler(nc, "ns")
	scope, _ := crewscope.New("ns", "pilot")
	h.publishCoordinatorError(scope, "conv-e", "the queue is full")
	got := strings.Join(readStream(t, gatewayServer(t, nc), "/api/v1/discussions/pilot/conv-e/stream", nil), "\n")
	if !strings.Contains(got, "the queue is full") || !strings.Contains(got, `"type":"done"`) {
		t.Fatalf("want the error as the answer, then done:\n%s", got)
	}
}

// Without a NATS connection a question is refused at once rather than lost.
func TestPostDiscussionWithNATSDownIsRefused(t *testing.T) {
	down := natsclient.NewClient("nats://127.0.0.1:1")
	if err := down.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(down.Close)
	srv := gatewayServer(t, down)
	resp, err := http.Post(srv.URL+"/api/v1/discussions/pilot", mimeJSON, strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	stream, err := http.Get(srv.URL + "/api/v1/discussions/pilot/conv-1/stream")
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Body.Close()
	if stream.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stream status %d, want 503", stream.StatusCode)
	}
	health, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status %d, want 200: liveness does not depend on NATS", health.StatusCode)
	}
}

func readyStatus(t *testing.T, nc *natsclient.Client) int {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(nc, "ns").RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	return rec.Code
}

// When the NATS subscription ends before the thread closes, the stream ends with an
// error the client answers by reconnecting.
func TestProcessMessagesReportsAnEndedSubscription(t *testing.T) {
	ch := make(chan jetstream.Msg)
	close(ch)
	err := processMessages(context.Background(), ch, &threadFinder{conversationID: "c"}, newMessageDeduper(), func(SSEEvent) {})
	if err != errSubscriptionEnded {
		t.Fatalf("err = %v, want errSubscriptionEnded", err)
	}
}

func itoa(n uint64) string {
	return strconv.FormatUint(n, 10)
}
