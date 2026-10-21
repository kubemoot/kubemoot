package bus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const testAgent = "hello"
const testdata = "../../../operator/pkg/bus/testdata/discussion-message/"
const schemaFile = "../../../operator/pkg/bus/schema/discussion-message.json"

type stubTurner struct {
	resp a2a.SendResponse
	err  error
	got  chan string
}

func (s *stubTurner) Do(_ context.Context, _, q string, _ []a2a.ThreadEntry, _ string) (a2a.SendResponse, error) {
	s.got <- q
	return s.resp, s.err
}

func startNATS(t *testing.T) *nats.Conn {
	t.Helper()
	s, err := server.NewServer(&server.Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	abs, _ := filepath.Abs(schemaFile)
	sch, err := jsonschema.NewCompiler().Compile(abs)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func recorded(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	raw, err := os.ReadFile(testdata + "advisory-ready.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	edit(m)
	out, _ := json.Marshal(m)
	return out
}

func setup(t *testing.T, turner Turner) (*nats.Conn, chan *nats.Msg, Scope) {
	t.Helper()
	nc := startNATS(t)
	scope := Scope{Namespace: "ns", Crew: "crew"}
	r := &Runner{Scope: scope, Agent: testAgent, Turner: turner, Registry: turn.NewRegistry(), Conn: nc, Deadline: 5 * time.Second}
	if _, err := r.Start(); err != nil {
		t.Fatal(err)
	}
	out := make(chan *nats.Msg, 8)
	if _, err := nc.ChanSubscribe(scope.Wildcard(), out); err != nil {
		t.Fatal(err)
	}
	return nc, out, scope
}

// next returns the next published message from the agent "hello".
func next(t *testing.T, out chan *nats.Msg) map[string]any {
	t.Helper()
	for {
		select {
		case m := <-out:
			var v map[string]any
			_ = json.Unmarshal(m.Data, &v)
			if v["agentName"] == testAgent {
				if err := schema(t).Validate(v); err != nil {
					t.Fatalf("published message breaks the bus contract: %v\n%s", err, m.Data)
				}
				return v
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no message from the agent")
		}
	}
}

func selecting(m map[string]any) {
	m["metadata"].(map[string]any)["innerCircle"] = []any{testAgent}
	m["metadata"].(map[string]any)["userQuery"] = "which nodes have a GPU?"
}

func TestSelectedAgentAnswersWithAContractSignal(t *testing.T) {
	done := a2a.SendResponse{Task: &a2a.Task{Artifacts: []a2a.Artifact{{Parts: []a2a.Part{{Text: "two nodes"}}}}}}
	done.Task.Status.State = "TASK_STATE_COMPLETED"
	st := &stubTurner{resp: done, got: make(chan string, 1)}
	nc, out, scope := setup(t, st)

	msg := recorded(t, selecting)
	_ = nc.Publish(scope.DiscussSubject("broadcast", "33627b88-c58c-4017-a483-119d2e45c712"), msg)

	if v := next(t, out); v["messageType"] != "evaluating" {
		t.Fatalf("first message %v", v["messageType"])
	}
	v := next(t, out)
	if v["messageType"] != "agree" || v["content"] != "two nodes" {
		t.Fatalf("got %v", v)
	}
	if q := <-st.got; q != "which nodes have a GPU?" {
		t.Fatalf("harness asked %q", q)
	}
	md := v["metadata"].(map[string]any)
	if _, ok := md["turnMs"]; !ok {
		t.Fatalf("metadata %v", md)
	}
}

func TestAgentNotInTheInnerCircleStaysQuiet(t *testing.T) {
	st := &stubTurner{got: make(chan string, 1)}
	nc, out, scope := setup(t, st)
	_ = nc.Publish(scope.DiscussSubject("broadcast", "t"), recorded(t, func(m map[string]any) {}))
	select {
	case <-st.got:
		t.Fatal("the harness was asked although this agent was not selected")
	case m := <-out:
		var v map[string]any
		_ = json.Unmarshal(m.Data, &v)
		if v["agentName"] == testAgent {
			t.Fatalf("unexpected message %v", v)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func TestDuplicateDeliveryRunsOneTurn(t *testing.T) {
	done := a2a.SendResponse{Message: &a2a.Message{Parts: []a2a.Part{{Text: "x"}}}}
	st := &stubTurner{resp: done, got: make(chan string, 4)}
	nc, out, scope := setup(t, st)
	msg := recorded(t, selecting)
	_ = nc.Publish(scope.DiscussSubject("broadcast", "t"), msg)
	_ = nc.Publish(scope.DiscussSubject("general", "t"), msg)
	next(t, out)
	next(t, out)
	time.Sleep(200 * time.Millisecond)
	if len(st.got) != 1 {
		t.Fatalf("turns run: %d", len(st.got)+0)
	}
}

func TestHarnessErrorBecomesAFailureSignalNotSilence(t *testing.T) {
	st := &stubTurner{err: errors.New("connection refused"), got: make(chan string, 1)}
	nc, out, scope := setup(t, st)
	_ = nc.Publish(scope.DiscussSubject("broadcast", "t"), recorded(t, selecting))
	next(t, out)
	v := next(t, out)
	md := v["metadata"].(map[string]any)
	if v["messageType"] != "failure" || md["failureType"] != "harness_error" {
		t.Fatalf("got %v", v)
	}
}
