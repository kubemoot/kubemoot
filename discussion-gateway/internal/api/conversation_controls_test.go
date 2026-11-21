package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	convCtl      = "conv-ctl"
	threadCtl    = "thread-ctl"
	convOther    = "conv-other"
	threadOther  = "thread-other"
	objectName   = "ns/pilot/" + threadCtl + "/agent/result-1"
	otherObject  = "ns/pilot/" + threadOther + "/agent/result-1"
	factKey      = "ns.pilot.topic.fact"
	otherFactKey = "ns.pilot.topic.other"
	reasonText   = "support ticket 77"
	agentGateway = "discussion-gateway"
	stamp        = "2026-11-20T10:00:00Z"
	wireResume   = "thread_resume"
)

// controlSetup is an owned gateway with every store a conversation lives in.
type controlSetup struct {
	ownedSetup
	coordinator jetstream.Stream
	scheduled   jetstream.KeyValue
	memory      jetstream.KeyValue
	workspace   jetstream.ObjectStore
}

func newControlSetup(t *testing.T) controlSetup {
	t.Helper()
	return withStores(t, newOwnedSetup(t, false), false)
}

// withStores adds the stores a conversation lives in. With denyPurge the coordinator log refuses
// to be purged, which makes a store the gateway cannot clear.
func withStores(t *testing.T, o ownedSetup, denyPurge bool) controlSetup {
	t.Helper()
	ctx := context.Background()
	c := controlSetup{ownedSetup: o}
	var err error
	if c.coordinator, err = o.js.CreateStream(ctx, jetstream.StreamConfig{
		Name: coordinatorStream, Subjects: []string{"kubemoot.coordinator.>"}, DenyPurge: denyPurge}); err != nil {
		t.Fatal(err)
	}
	if c.scheduled, err = o.js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: scheduledBucket}); err != nil {
		t.Fatal(err)
	}
	if c.memory, err = o.js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: crewmemory.Bucket}); err != nil {
		t.Fatal(err)
	}
	if c.workspace, err = o.js.ObjectStore(ctx, workspaceBucket); err != nil {
		t.Fatal(err)
	}
	return c
}

// seed gives a conversation a record in every store: an owner, a history of one answered
// turn, the thread's messages (left open when open), its coordinator log, a workspace object,
// a reminder scheduled from the thread and a memory fact learned in it.
func (c controlSetup) seed(t *testing.T, token, conv, thread, object, fact string, open bool) {
	t.Helper()
	ctx := context.Background()
	if code := c.ask(t, token, conv); code != http.StatusOK {
		t.Fatalf("owner of %s: %d", conv, code)
	}
	history, _ := json.Marshal(bus.ConversationHistory{LastAccessed: stamp, Turns: []bus.StoredTurn{
		{ThreadID: thread, Query: "q", Response: "a", Timestamp: stamp}}})
	if _, err := c.kv.Put(ctx, bus.ConversationHistoryKey("ns", "pilot", conv), history); err != nil {
		t.Fatal(err)
	}
	publishDiscuss(t, c.js, turnStart(thread, conv, "q of "+conv))
	publishDiscuss(t, c.js, msgOf(thread, thread+"-agree", signalAgree, "x"))
	if !open {
		publishDiscuss(t, c.js, msgOf(thread, thread+"-s", msgSynthesis, "answer"))
		publishDiscuss(t, c.js, msgOf(thread, thread+"-c", msgThreadClose, ""))
	}
	for _, n := range []string{"1", "2"} {
		if _, err := c.js.Publish(ctx, "kubemoot.coordinator.ns.pilot."+thread, []byte(`{"n":`+n+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.workspace.PutBytes(ctx, object, []byte("tool result")); err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(bus.ScheduledRecord{ScheduleID: "sch-" + thread, Namespace: "ns", Crew: "pilot",
		TriggerAt: "2030-01-01T00:00:00Z", SourceThreadID: thread, Message: "later"})
	if _, err := c.scheduled.Put(ctx, "sch-"+thread, rec); err != nil {
		t.Fatal(err)
	}
	fv, _ := json.Marshal(bus.CrewMemoryFact{Value: "v", LearnedBy: "a", LearnedInThread: thread, LearnedAt: stamp})
	if _, err := c.memory.Put(ctx, fact, fv); err != nil {
		t.Fatal(err)
	}
}

func (c controlSetup) conversationPath(conv, suffix string) string {
	return c.srv.URL + listPath + "/" + conv + suffix
}

// lastOn is the last message the discussion stream holds on subject.
func (c controlSetup) lastOn(t *testing.T, subject string) bus.DiscussionMessage {
	t.Helper()
	stream, err := c.js.Stream(context.Background(), streamName)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := stream.GetLastMsgForSubject(context.Background(), subject)
	if err != nil {
		t.Fatalf("no message on %s: %v", subject, err)
	}
	var m bus.DiscussionMessage
	if err := json.Unmarshal(raw.Data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// held counts what every store holds of a conversation: history, owner, stream messages,
// coordinator events, workspace objects, schedules and memory facts.
func (c controlSetup) held(t *testing.T, conv, thread, object, fact string) []int {
	t.Helper()
	ctx := context.Background()
	has := func(kv jetstream.KeyValue, key string) int {
		if _, err := kv.Get(ctx, key); err == nil {
			return 1
		}
		return 0
	}
	count := func(stream jetstream.Stream, subject string) int {
		info, err := stream.Info(ctx, jetstream.WithSubjectFilter(subject))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, c := range info.State.Subjects {
			n += int(c)
		}
		return n
	}
	discuss, _ := c.js.Stream(ctx, streamName)
	objects := 0
	if _, err := c.workspace.GetInfo(ctx, object); err == nil {
		objects = 1
	}
	return []int{
		has(c.kv, bus.ConversationHistoryKey("ns", "pilot", conv)),
		has(c.kv, bus.ConversationOwnerKey("ns", "pilot", conv)),
		count(discuss, "kubemoot.discuss.ns.pilot.*."+thread),
		count(c.coordinator, "kubemoot.coordinator.ns.pilot."+thread),
		objects,
		has(c.scheduled, "sch-"+thread),
		has(c.memory, fact),
	}
}

func sum(counts []int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

var controls = []struct {
	name, path, messageType string
}{
	{"stop", "/stop", "stop_requested"},
	{"pause", "/pause", "thread_pause"},
	{"resume", "/resume", wireResume},
}

func TestTheOwnerMayStopPauseAndResumeAndTheGatewaySendsTheMessageAsItself(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, true)
	for _, ctl := range controls {
		got := c.controlAs(t, tokenAlice, ctl.name, ctl.path)
		if string(got.Action) != ctl.name || got.ConversationID != convCtl || strings.Join(got.Threads, ",") != threadCtl {
			t.Fatalf("%s: answered %+v", ctl.name, got)
		}
		m := c.lastOn(t, "kubemoot.discuss.ns.pilot.broadcast."+threadCtl)
		assertSentByTheGateway(t, m, ctl.messageType)
	}
}

// assertSentByTheGateway checks a control message is the gateway's own, never a person's.
func assertSentByTheGateway(t *testing.T, m bus.DiscussionMessage, messageType string) {
	t.Helper()
	got := [3]string{string(m.MessageType), m.ThreadID, m.AgentName}
	if got != [3]string{messageType, threadCtl, agentGateway} || m.MessageID == "" || m.Timestamp == "" {
		t.Fatalf("message %+v", m)
	}
}

// controlAs sends a control as token and decodes the 200 answer.
func (c controlSetup) controlAs(t *testing.T, token, name, path string) bus.ConversationControl {
	t.Helper()
	resp := do(t, http.MethodPost, c.conversationPath(convCtl, path), token, "")
	var got bus.ConversationControl
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %v", name, resp.StatusCode, err)
	}
	return got
}

func TestTheWireNamesOfTheControlsAreTheCoordinatorsVocabulary(t *testing.T) {
	for _, ctl := range []controlOf{stopControl, pauseControl, resumeControl} {
		want := map[bus.ConversationControlAction]string{
			bus.ConversationControlActionStop: "stop_requested", bus.ConversationControlActionPause: "thread_pause",
			bus.ConversationControlActionResume: "thread_resume"}[ctl.action]
		if string(ctl.messageType) != want {
			t.Errorf("%s sends %q, the coordinator reads %q", ctl.action, ctl.messageType, want)
		}
	}
	if m := stopControl.messageType; m != bus.DiscussionMessageMessageTypeStopRequested {
		t.Fatalf("stop sends %s", m)
	}
}

func TestStopReasonIsManualStop(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, true)
	do(t, http.MethodPost, c.conversationPath(convCtl, "/stop"), tokenAlice, "")
	m := c.lastOn(t, "kubemoot.discuss.ns.pilot.broadcast."+threadCtl)
	if m.Metadata[metaReason] != "manual_stop" {
		t.Fatalf("metadata %+v", m.Metadata)
	}
}

func TestAControlIsRefusedToEveryoneButTheOwnerAndAnOperatorWithAReasonAndTheRole(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, true)
	for _, ctl := range controls {
		url := c.conversationPath(convCtl, ctl.path)
		for _, tc := range []struct {
			who    string
			token  string
			header []string
			want   int
		}{
			{"another caller", tokenBob, nil, http.StatusNotFound},
			{"an operator with no reason", tokenAdmin, nil, http.StatusBadRequest},
			{"an operator who may only read", tokenReader, []string{reasonHeader, reasonText}, http.StatusNotFound},
			{"a caller who may not use the crew", tokenNoRole, nil, http.StatusForbidden},
			{"no credential", "", nil, http.StatusUnauthorized},
			{"an operator with a reason", tokenAdmin, []string{reasonHeader, reasonText}, http.StatusOK},
		} {
			if got := do(t, http.MethodPost, url, tc.token, "", tc.header...).StatusCode; got != tc.want {
				t.Errorf("%s by %s: %d, want %d", ctl.name, tc.who, got, tc.want)
			}
		}
	}
}

func TestAControlAnswersAFinishedOrEmptyConversationWithItsState(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
	if got := do(t, http.MethodPost, c.conversationPath(convCtl, "/stop"), tokenAlice, "").StatusCode; got != http.StatusConflict {
		t.Fatalf("a finished conversation has nothing to stop: %d, want 409", got)
	}
	c.ask(t, tokenAlice, "conv-empty")
	if got := do(t, http.MethodPost, c.conversationPath("conv-empty", "/pause"), tokenAlice, "").StatusCode; got != http.StatusNotFound {
		t.Fatalf("a conversation with no thread: %d, want 404", got)
	}
	if got := do(t, http.MethodPost, c.conversationPath("bad.id", "/stop"), tokenAlice, "").StatusCode; got != http.StatusBadRequest {
		t.Fatalf("a bad id: %d, want 400", got)
	}
}

func TestAnAnonymousCrewControlsAnyConversationAsItAlwaysCouldNotBefore(t *testing.T) {
	nc, js := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	publishDiscuss(t, js, turnStart("t1", "conv-1", "q"))
	for _, ctl := range controls {
		if got := do(t, http.MethodPost, srv.URL+listPath+"/conv-1"+ctl.path, "", "").StatusCode; got != http.StatusOK {
			t.Errorf("%s: %d", ctl.name, got)
		}
	}
	if got := do(t, http.MethodPost, srv.URL+listPath+"/nobody/stop", "", "").StatusCode; got != http.StatusNotFound {
		t.Errorf("an unknown conversation: %d", got)
	}
}

func TestDeletingARunningConversationNeedsForce(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, true)
	if got := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAlice, "").StatusCode; got != http.StatusConflict {
		t.Fatalf("%d, want 409", got)
	}
	if sum(c.held(t, convCtl, threadCtl, objectName, factKey)) == 0 {
		t.Fatal("a refused delete removed something")
	}
	if got := do(t, http.MethodDelete, c.conversationPath(convCtl, "?force=true"), tokenAlice, "").StatusCode; got != http.StatusOK {
		t.Fatalf("forced: %d", got)
	}
}

func TestDeleteRemovesTheConversationFromEveryStoreAndReportsIt(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
	c.seed(t, tokenBob, convOther, threadOther, otherObject, otherFactKey, false)
	if before := c.held(t, convCtl, threadCtl, objectName, factKey); !slices.Equal(before, fullyHeld) {
		t.Fatalf("the seed is incomplete: %v, want %v", before, fullyHeld)
	}

	resp := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAlice, "")
	var got bus.ConversationErasure
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, err)
	}
	assertFullReport(t, got)
	if after := c.held(t, convCtl, threadCtl, objectName, factKey); sum(after) != 0 {
		t.Fatalf("still held after the delete: %v", after)
	}
	if other := c.held(t, convOther, threadOther, otherObject, otherFactKey); !slices.Equal(other, fullyHeld) {
		t.Fatalf("another conversation lost something: %v, want %v", other, fullyHeld)
	}
	// Gone is gone: the owner asks again and is told it does not exist.
	if got := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAlice, "").StatusCode; got != http.StatusNotFound {
		t.Fatalf("a second delete: %d, want 404", got)
	}
	if code, list := c.list(t, tokenAlice, ""); code != http.StatusOK || strings.Contains(ids(list), convCtl) {
		t.Fatalf("the list still shows it: %d %s", code, ids(list))
	}
}

// fullyHeld is what a seeded conversation holds: a history, an owner, four discussion messages,
// two coordinator events, a workspace object, a schedule and a memory fact.
var fullyHeld = []int{1, 1, 4, 2, 1, 1, 1}

// assertFullReport checks a delete of the seeded conversation reported every store.
func assertFullReport(t *testing.T, got bus.ConversationErasure) {
	t.Helper()
	want := bus.ConversationErasureDeleted{History: 1, Owner: 1, DiscussionMessages: 4, CoordinatorEvents: 2,
		WorkspaceObjects: 1, Scheduled: 1, MemoryFacts: 1}
	if got.Deleted != want || got.ConversationID != convCtl || strings.Join(got.Threads, ",") != threadCtl ||
		len(got.Failed) != 0 || len(got.Kept) == 0 {
		t.Fatalf("report %+v, want deleted %+v", got, want)
	}
}

func TestDeleteIsRefusedToEveryoneButTheOwnerAndAnOperatorWithAReasonAndTheRole(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
	for _, tc := range []struct {
		who    string
		token  string
		header []string
		want   int
	}{
		{"another caller", tokenBob, nil, http.StatusNotFound},
		{"an operator with no reason", tokenAdmin, nil, http.StatusBadRequest},
		{"an operator who may only read", tokenReader, []string{reasonHeader, reasonText}, http.StatusNotFound},
		{"no credential", "", nil, http.StatusUnauthorized},
	} {
		if got := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tc.token, "", tc.header...).StatusCode; got != tc.want {
			t.Errorf("%s: %d, want %d", tc.who, got, tc.want)
		}
		if sum(c.held(t, convCtl, threadCtl, objectName, factKey)) < sum(fullyHeld) {
			t.Fatalf("%s: a refused delete removed something", tc.who)
		}
	}
	got := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAdmin, "", reasonHeader, reasonText)
	if got.StatusCode != http.StatusOK || sum(c.held(t, convCtl, threadCtl, objectName, factKey)) != 0 {
		t.Fatalf("an operator with a reason: %d", got.StatusCode)
	}
}

func TestOperatorUseOfAControlIsAudited(t *testing.T) {
	var lines []string
	prev := auditLog
	auditLog = funcr.New(func(prefix, args string) { lines = append(lines, prefix+args) }, funcr.Options{})
	t.Cleanup(func() { auditLog = prev })
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, true)
	do(t, http.MethodPost, c.conversationPath(convCtl, "/pause"), tokenAlice, "")
	do(t, http.MethodPost, c.conversationPath(convCtl, "/pause"), tokenAdmin, "", reasonHeader, reasonText)
	do(t, http.MethodDelete, c.conversationPath(convCtl, "?force=true"), tokenAdmin, "", reasonHeader, reasonText)
	if len(lines) != 2 {
		t.Fatalf("one record per operator use, none for the owner: %v", lines)
	}
	for i, op := range []string{`"operation"="pause"`, `"operation"="delete"`} {
		for _, want := range []string{identity.AuditEvent, `"operator"="admin"`, op, convCtl, reasonText} {
			if !strings.Contains(lines[i], want) {
				t.Errorf("record %d lacks %q: %s", i, want, lines[i])
			}
		}
	}
}

func TestAnAnonymousCrewDeletesAConversationAndReportsWhatItRemoved(t *testing.T) {
	nc, js := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	publishDiscuss(t, js, turnStart("t1", "conv-1", "q"))
	publishDiscuss(t, js, msgOf("t1", "c1", msgThreadClose, ""))
	resp := do(t, http.MethodDelete, srv.URL+listPath+"/conv-1", "", "")
	var got bus.ConversationErasure
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK ||
		got.Deleted.DiscussionMessages != 2 || strings.Join(got.Threads, ",") != "t1" {
		t.Fatalf("%d %+v %v", resp.StatusCode, got, err)
	}
	if got := do(t, http.MethodDelete, srv.URL+listPath+"/conv-1", "", "").StatusCode; got != http.StatusNotFound {
		t.Fatalf("nothing left to delete: %d, want 404", got)
	}
}

func TestADeleteThatCannotClearAStoreSaysSoAndKeepsTheOwnerRecordToRepeatIt(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := newEraser(c.js, pilotScope(), convCtl).erase(ctx, []string{threadCtl})
	if len(report.Failed) < 5 {
		t.Fatalf("every store failed to answer, failed %v", report.Failed)
	}
	if held := c.held(t, convCtl, threadCtl, objectName, factKey); sum(held) < sum(fullyHeld) {
		t.Fatalf("a delete that could not clear removed %v", held)
	}
	// The repeat, with the stores answering, finishes the job.
	repeat := newEraser(c.js, pilotScope(), convCtl).erase(context.Background(), []string{threadCtl})
	if len(repeat.Failed) != 0 || sum(c.held(t, convCtl, threadCtl, objectName, factKey)) != 0 {
		t.Fatalf("repeat %+v", repeat)
	}
}

func TestTheEraserCountsAndSkipsWhatIsNotThere(t *testing.T) {
	_, js := jetStreamServer(t)
	e := newEraser(js, pilotScope(), "none")
	report := e.erase(context.Background(), nil)
	if !report.nothingFound() || len(report.Failed) != 0 {
		t.Fatalf("a conversation in no store: %+v", report)
	}
}

// A history is data the gateway did not write: a thread id in it that is a wildcard must not
// widen a purge to the crew's other conversations.
func TestAWildcardThreadInAHistoryNeverReachesAnotherConversation(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenBob, convOther, threadOther, otherObject, otherFactKey, false)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
	for _, evil := range []string{">", "*", "x.y", "../" + threadOther} {
		history, _ := json.Marshal(bus.ConversationHistory{LastAccessed: stamp, Turns: []bus.StoredTurn{
			{ThreadID: evil, Query: "q", Response: "a", Timestamp: stamp}, {ThreadID: threadCtl, Timestamp: stamp}}})
		if _, err := c.kv.Put(context.Background(), bus.ConversationHistoryKey("ns", "pilot", convCtl), history); err != nil {
			t.Fatal(err)
		}
		report := newEraser(c.js, pilotScope(), convCtl).erase(context.Background(), []string{evil, threadCtl})
		if strings.Join(report.Threads, ",") != threadCtl {
			t.Fatalf("thread %q was used: %v", evil, report.Threads)
		}
		if other := c.held(t, convOther, threadOther, otherObject, otherFactKey); !slices.Equal(other, fullyHeld) {
			t.Fatalf("thread %q reached another conversation: %v", evil, other)
		}
		c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
	}
}

// One store that cannot be cleared leaves the history and the owner record, so the owner can
// repeat the delete; the other stores are cleared, and the repeat finishes the job.
func TestADeleteWithOneStoreThatCannotBeClearedKeepsTheHistoryAndTheOwnerToRepeatIt(t *testing.T) {
	c := withStores(t, newOwnedSetup(t, false), true)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)

	resp := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAlice, "")
	var got bus.ConversationErasure
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d %v, want 500", resp.StatusCode, err)
	}
	if strings.Join(got.Failed, ",") != "coordinator log" || got.Deleted.History != 0 || got.Deleted.Owner != 0 {
		t.Fatalf("report %+v", got)
	}
	held := c.held(t, convCtl, threadCtl, objectName, factKey)
	if want := []int{1, 1, 0, 2, 0, 0, 0}; !slices.Equal(held, want) {
		t.Fatalf("held %v, want %v: the other stores cleared, the history and owner kept", held, want)
	}
	// The owner can repeat it; the log is still refusing, so it fails the same way, never leaving the owner locked out.
	if got := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAlice, "").StatusCode; got != http.StatusInternalServerError {
		t.Fatalf("the repeat: %d", got)
	}
}

// The same thread id in another crew, and another conversation of the same crew, keep everything.
func TestADeleteLeavesAnotherCrewsAndAnotherConversationsDataWithTheSameThreadId(t *testing.T) {
	c := newControlSetup(t)
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
	publishOn(t, c.js, "kubemoot.discuss.ns.other.broadcast."+threadCtl, turnStart(threadCtl, "conv-x", "q"))
	if _, err := c.js.Publish(context.Background(), "kubemoot.coordinator.ns.other."+threadCtl, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.workspace.PutBytes(context.Background(), "ns/other/"+threadCtl+"/agent/o", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAlice, "").StatusCode; got != http.StatusOK {
		t.Fatalf("delete %d", got)
	}
	discuss, _ := c.js.Stream(context.Background(), streamName)
	info, _ := discuss.Info(context.Background(), jetstream.WithSubjectFilter("kubemoot.discuss.ns.other.>"))
	if info.State.Subjects["kubemoot.discuss.ns.other.broadcast."+threadCtl] != 1 {
		t.Fatalf("another crew's messages: %v", info.State.Subjects)
	}
	log, _ := c.coordinator.Info(context.Background(), jetstream.WithSubjectFilter("kubemoot.coordinator.ns.other.>"))
	if log.State.Msgs < 1 {
		t.Fatal("another crew's coordinator log was removed")
	}
	if _, err := c.workspace.GetInfo(context.Background(), "ns/other/"+threadCtl+"/agent/o"); err != nil {
		t.Fatalf("another crew's workspace object: %v", err)
	}
}
