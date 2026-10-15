package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/nats-io/nats.go/jetstream"
)

// publishOn publishes one discussion message on subject.
func publishOn(t *testing.T, js jetstream.JetStream, subject string, msg map[string]interface{}) {
	t.Helper()
	data, _ := json.Marshal(msg)
	if _, err := js.Publish(context.Background(), subject, data); err != nil {
		t.Fatal(err)
	}
}

// turnStart is a thread_start of conversation conv asking question.
func turnStart(thread, conv, question string) map[string]interface{} {
	meta := map[string]interface{}{metaConversationID: conv}
	if question != "" {
		meta["userQuery"] = question
	}
	m := startOf(thread)
	m["metadata"] = meta
	return m
}

func getJSON(t *testing.T, url string, v interface{}) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestListConversationsNewestFirstWithTheFirstQuestion(t *testing.T) {
	nc, js := jetStreamServer(t)
	publishDiscuss(t, js, turnStart("t1", "conv-a", "Describe the proxmox nodes."))
	publishDiscuss(t, js, turnStart("t2", "conv-b", "What is failing?"))
	publishDiscuss(t, js, msgOf("t2", "m1", signalAgree, "noise on the broadcast channel"))
	publishDiscuss(t, js, turnStart("t3", "conv-a", "And their memory?"))
	// Another crew and another namespace are not listed.
	publishOn(t, js, "kubemoot.discuss.ns.other.broadcast.t4", turnStart("t4", "conv-other", "hidden"))
	publishOn(t, js, "kubemoot.discuss.ns2.pilot.broadcast.t5", turnStart("t5", "conv-ns2", "hidden"))
	srv := gatewayServer(t, nc)

	var got conversationList
	if code := getJSON(t, srv.URL+"/api/v1/discussions/pilot", &got); code != http.StatusOK || len(got.Conversations) != 2 {
		t.Fatalf("status %d, want 2 conversations: %+v", code, got.Conversations)
	}
	assertNewestFirst(t, got.Conversations)

	var limited conversationList
	getJSON(t, srv.URL+"/api/v1/discussions/pilot?limit=1", &limited)
	if len(limited.Conversations) != 1 || limited.Conversations[0].ConversationID != "conv-a" {
		t.Fatalf("limit=1: got %+v", limited.Conversations)
	}
}

// assertNewestFirst checks conv-a, asked twice and last, is first with its first question.
func assertNewestFirst(t *testing.T, got []ConversationSummary) {
	t.Helper()
	a, b := got[0], got[1]
	if a.ConversationID != "conv-a" || a.Question != "Describe the proxmox nodes." || a.Turns != 2 || b.ConversationID != "conv-b" {
		t.Fatalf("got %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, a.StartedAt); err != nil || a.LastTurnAt < a.StartedAt {
		t.Fatalf("times: %+v", a)
	}
}

func TestListConversationsOfAnEmptyCrewIsAnEmptyList(t *testing.T) {
	nc, _ := jetStreamServer(t)
	resp, err := http.Get(gatewayServer(t, nc).URL + "/api/v1/discussions/pilot")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != `{"conversations":[]}` {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}

func TestListConversationsRejectsABadLimitOrCrew(t *testing.T) {
	nc, _ := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	for _, path := range []string{"/api/v1/discussions/pilot?limit=abc", "/api/v1/discussions/pilot?limit=0", "/api/v1/discussions/a.b"} {
		if code := getJSON(t, srv.URL+path, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, code)
		}
	}
}

func TestParseLimit(t *testing.T) {
	cases := map[string]int{"": defaultConversationLimit, "5": 5, "1000": maxConversationLimit}
	for raw, want := range cases {
		if got, err := parseLimit(raw); err != nil || got != want {
			t.Errorf("%q: got %d, %v", raw, got, err)
		}
	}
	for _, bad := range []string{"-1", "0", "x"} {
		if _, err := parseLimit(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// The first question is the earliest turn's, whatever order the turns are read in, and
// a turn without a question does not hide one that has it.
func TestConversationIndexKeepsTheEarliestQuestion(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	idx := conversationIndex{}
	idx.observe(discussMsg{data: natsMessage{MessageType: msgThreadStart, Content: "second", Metadata: map[string]interface{}{metaConversationID: "c"}}, published: t0.Add(time.Minute)})
	idx.observe(discussMsg{data: natsMessage{MessageType: msgThreadStart, Metadata: map[string]interface{}{metaConversationID: "c"}}, published: t0})
	idx.observe(discussMsg{data: natsMessage{MessageType: msgThreadStart}, published: t0})
	idx.observe(discussMsg{data: natsMessage{MessageType: signalAgree, Metadata: map[string]interface{}{metaConversationID: "c"}}, published: t0})
	got := idx.newestFirst(10)
	if len(got) != 1 || got[0].Question != "second" || got[0].Turns != 2 || got[0].StartedAt != testStamp {
		t.Fatalf("got %+v", got)
	}
}

// artifactStore creates the artifact bucket and puts content at key.
func artifactStore(t *testing.T, js jetstream.JetStream, key, content string) jetstream.ObjectStore {
	t.Helper()
	store, err := js.CreateObjectStore(context.Background(), jetstream.ObjectStoreConfig{Bucket: artifactBucket})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutString(context.Background(), key, content); err != nil {
		t.Fatal(err)
	}
	return store
}

func getArtifactBody(t *testing.T, url string) (int, string, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header.Get("Content-Length")
}

func TestArtifactIsFetchedWhileItExistsAndNotFoundAfter(t *testing.T) {
	nc, js := jetStreamServer(t)
	const key = "ns/pilot/t1/k8s/agree-1b2c"
	store := artifactStore(t, js, key, "node pve1 online\nnode pve2 online\n")
	url := gatewayServer(t, nc).URL + "/api/v1/discussions/pilot/artifact?key=" + key

	code, body, length := getArtifactBody(t, url)
	if code != http.StatusOK || body != "node pve1 online\nnode pve2 online\n" || length != "34" {
		t.Fatalf("status %d length %s body %q", code, length, body)
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := getArtifactBody(t, url); code != http.StatusNotFound || !strings.Contains(body, "deleted after it closes") {
		t.Fatalf("after delete: status %d body %s", code, body)
	}
}

func TestArtifactOutsideTheCrewIsNotFound(t *testing.T) {
	nc, js := jetStreamServer(t)
	artifactStore(t, js, "ns2/pilot/t1/k8s/agree-2", "another namespace")
	srv := gatewayServer(t, nc)
	for _, key := range []string{"ns2/pilot/t1/k8s/agree-2", "ns/other/t1/k8s/agree-1", "ns/pilot/t1/../x/y"} {
		if code, body, _ := getArtifactBody(t, srv.URL+"/api/v1/discussions/pilot/artifact?key="+key); code != http.StatusNotFound || strings.Contains(body, "another namespace") {
			t.Errorf("%s: status %d body %s", key, code, body)
		}
	}
	if code, _, _ := getArtifactBody(t, srv.URL+"/api/v1/discussions/pilot/artifact"); code != http.StatusBadRequest {
		t.Errorf("missing key: status %d, want 400", code)
	}
}

func TestArtifactWithoutABucketIsNotFound(t *testing.T) {
	nc, _ := jetStreamServer(t)
	code, _, _ := getArtifactBody(t, gatewayServer(t, nc).URL+"/api/v1/discussions/pilot/artifact?key=ns/pilot/t1/k8s/a")
	if code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", code)
	}
}

func TestInScope(t *testing.T) {
	scope, _ := crewscope.New("ns", "pilot")
	cases := map[string]bool{
		"ns/pilot/t1/k8s/agree-1": true,
		"ns/pilot/t1/k8s":         false,
		"ns/pilot/t1/k8s/a/b":     false,
		"ns/pilot//k8s/a":         false,
		"ns/pilot/t1/../a":        false,
		"ns/pilotx/t1/k8s/a":      false,
		"other/pilot/t1/k8s/a":    false,
	}
	for key, want := range cases {
		if got := inScope(scope, key); got != want {
			t.Errorf("%s: got %v, want %v", key, got, want)
		}
	}
}

// Every event made from a message carries the message's own timestamp; thread_found
// carries the thread_start's.
func TestStreamEventsCarryTheMessageTimestamp(t *testing.T) {
	nc, js := jetStreamServer(t)
	start := startOf("A")
	start["timestamp"] = testStamp
	publishDiscuss(t, js, start)
	agree := msgOf("A", "a1", signalAgree, "fine")
	agree["timestamp"] = "2026-10-15T12:00:07.25Z"
	agree["metadata"] = map[string]interface{}{metaInputTokens: 10, metaOutputTokens: 2}
	publishDiscuss(t, js, agree)
	publishDiscuss(t, js, msgOf("A", "a2", "thread_close", ""))

	got := strings.Join(readStream(t, gatewayServer(t, nc), "/api/v1/discussions/pilot/conv-1/stream", nil), "\n")
	for _, want := range []string{
		`"type":"thread_found","threadId":"A","timestamp":"` + testStamp + `"`,
		`"timestamp":"2026-10-15T12:00:07.25Z","inputTokens":10,"outputTokens":2`,
		`"type":"done","timestamp":"20`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stream lacks %s:\n%s", want, got)
		}
	}
}

// A list whose request is gone stops reading the stream.
func TestReadPendingStopsWhenTheContextIsDone(t *testing.T) {
	_, js := jetStreamServer(t)
	publishDiscuss(t, js, turnStart("t1", "conv-a", "q"))
	consumer, err := js.CreateConsumer(context.Background(), streamName, jetstream.ConsumerConfig{AckPolicy: jetstream.AckNonePolicy})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	read := 0
	if err := readPending(ctx, consumer, 1, func(discussMsg) { read++ }); !errors.Is(err, context.Canceled) || read != 0 {
		t.Fatalf("err %v, read %d", err, read)
	}
	if err := readPending(context.Background(), consumer, 1, func(discussMsg) { read++ }); err != nil || read != 1 {
		t.Fatalf("err %v, read %d", err, read)
	}
}
