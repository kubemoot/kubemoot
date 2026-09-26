package liaison

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type staticLister struct {
	crews []Crew
	err   error
}

func (l staticLister) List(context.Context) ([]Crew, error) { return l.crews, l.err }

func synthesisEvents() []string {
	return []string{
		`{"type":"connected"}`,
		`{"type":"finding","agent":"k8s-tooler","signal":"agree","summary":"private"}`,
		`{"type":"finding","agent":"obs-tooler","signal":"concern","summary":"private"}`,
		`{"type":"synthesis","content":"The capital of France is Paris."}`,
		`{"type":"done"}`,
	}
}

func newTestService(t *testing.T, f *fakeGateway, maxInflight int) *Service {
	t.Helper()
	crews := staticLister{crews: []Crew{{Name: "hello", Namespace: "hello", Ready: true}, {Name: "dup", Namespace: "a"}, {Name: "dup", Namespace: "b"}}}
	return NewService(crews, NewGatewayAt(f.resolve), NewStore(time.Hour), maxInflight)
}

func settle(t *testing.T, svc *Service, ticket string) TicketView {
	t.Helper()
	tk, ok := svc.tickets.Get(ticket)
	if !ok {
		t.Fatalf("ticket %s missing", ticket)
	}
	select {
	case <-tk.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("ticket never settled")
	}
	return tk.Snapshot()
}

func TestAskWithWaitReturnsAnswer(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 2)

	res, out, err := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "Capital of France?", WaitSeconds: sec(5)})
	if err != nil || res != nil {
		t.Fatalf("Ask: res=%v err=%v", res, err)
	}
	if out.State != StateAnswered || !strings.Contains(out.Answer, "Paris") || out.Contributions != 2 || out.Hint != "" {
		t.Fatalf("answered output: %+v", out)
	}
	// Nothing private crosses the boundary.
	for _, word := range []string{"k8s-tooler", "obs-tooler", "agree", "concern", "private"} {
		if strings.Contains(out.Answer, word) {
			t.Fatalf("answer leaks %q", word)
		}
	}
}

func TestAskThenGetAnswer(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 2)

	_, out, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "Capital of France?", WaitSeconds: sec(0)})
	if out.ID == "" || out.Hint != hintPending {
		t.Fatalf("pending output should carry a ticket and the YDY hint: %+v", out)
	}
	settle(t, svc, out.ID)
	res, got, _ := svc.GetAnswer(context.Background(), nil, GetAnswerInput{Ticket: out.ID})
	if res != nil || got.State != StateAnswered || !strings.Contains(got.Answer, "Paris") || got.Hint != "" {
		t.Fatalf("get_answer: res=%v out=%+v", res, got)
	}
}

func TestGetAnswerUnknownTicket(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	res, _, err := svc.GetAnswer(context.Background(), nil, GetAnswerInput{Ticket: "nope"})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("want tool error, got res=%v err=%v", res, err)
	}
}

func TestAskValidation(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	cases := map[string]AskInput{
		"empty question": {Crew: "hello"},
		"unknown crew":   {Crew: "ghost", Question: "q"},
		"ambiguous crew": {Crew: "dup", Question: "q"},
	}
	for name, in := range cases {
		res, _, err := svc.Ask(context.Background(), nil, in)
		if err != nil || res == nil || !res.IsError {
			t.Errorf("%s: want tool error, got res=%v err=%v", name, res, err)
		}
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("invalid asks reached the gateway: %v", got)
	}
}

func TestAskAmbiguityResolvedByNamespace(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	res, out, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "dup", Namespace: "b", Question: "q", WaitSeconds: sec(5)})
	if res != nil || out.Namespace != "b" || out.State != StateAnswered {
		t.Fatalf("namespaced ask: res=%v out=%+v", res, out)
	}
}

func TestAskInflightLimit(t *testing.T) {
	// A stream that never synthesizes keeps its slot until the follower's deadline.
	f := newFakeGateway(`{"type":"connected"}`)
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	svc.gateway = NewGatewayAt(func(namespace, crew string) string { return f.resolve(namespace, crew) })

	// Hold the single slot by taking it directly, as a running discussion would.
	svc.inflight <- struct{}{}
	res, _, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "q", WaitSeconds: sec(0)})
	if res == nil || !res.IsError || !strings.Contains(textOf(res), "limit") {
		t.Fatalf("want limit error, got %v", res)
	}
	<-svc.inflight
}

func TestFollowFailsWithoutSynthesis(t *testing.T) {
	f := newFakeGateway(`{"type":"connected"}`, `{"type":"done"}`)
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	_, out, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "q", WaitSeconds: sec(0)})
	v := settle(t, svc, out.ID)
	if v.State != StateFailed || !strings.Contains(v.Error, "without a synthesis") {
		t.Fatalf("want failed ticket, got %+v", v)
	}
	// The slot is free again.
	select {
	case svc.inflight <- struct{}{}:
		<-svc.inflight
	default:
		t.Fatal("in-flight slot not released")
	}
}

func TestFollowReportsGatewayError(t *testing.T) {
	f := newFakeGateway(`{"type":"error","error":"Discussion stream not available"}`)
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	_, out, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "q", WaitSeconds: sec(5)})
	if out.State != StateFailed || out.Error != "Discussion stream not available" {
		t.Fatalf("want gateway error surfaced, got %+v", out)
	}
}

func TestListCrewsError(t *testing.T) {
	svc := NewService(staticLister{err: errors.New("boom")}, NewGateway(), NewStore(time.Hour), 1)
	res, _, err := svc.ListCrews(context.Background(), nil, ListCrewsInput{})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("want tool error, got res=%v err=%v", res, err)
	}
}

func sec(n int) *int { return &n }

func TestWaitForBounds(t *testing.T) {
	if waitFor(nil) != defaultWait || waitFor(sec(0)) != 0 || waitFor(sec(-3)) != 0 || waitFor(sec(10)) != 10*time.Second || waitFor(sec(9999)) != maxWait {
		t.Fatal("waitFor bounds")
	}
}

func TestAskRejoinsPendingQuestion(t *testing.T) {
	f := newFakeGateway(`{"type":"connected"}`) // never settles within the test
	defer f.srv.Close()
	svc := newTestService(t, f, 4)
	_, first, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "same?", WaitSeconds: sec(0)})
	_, again, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "  same?  ", WaitSeconds: sec(0)})
	if first.ID == "" || again.ID != first.ID {
		t.Fatalf("retry started a new discussion: %s vs %s", first.ID, again.ID)
	}
	_, other, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "different?", WaitSeconds: sec(0)})
	if other.ID == first.ID {
		t.Fatal("a different question reused the ticket")
	}
	if got := f.asked(); len(got) != 2 {
		t.Fatalf("gateway asked %d times, want 2", len(got))
	}
}

func TestGetAnswerLongPolls(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	_, out, _ := svc.Ask(context.Background(), nil, AskInput{Crew: "hello", Question: "q", WaitSeconds: sec(0)})
	start := time.Now()
	_, got, _ := svc.GetAnswer(context.Background(), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(10)})
	if got.State != StateAnswered || time.Since(start) > 5*time.Second {
		t.Fatalf("long poll should return as soon as the answer exists: %+v after %s", got, time.Since(start))
	}
}
