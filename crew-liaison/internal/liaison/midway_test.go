package liaison

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	needsState = `{"conversationId":"conv-42","state":"finished","turns":[` +
		`{"threadId":"t1","state":"finished","needsAccounts":["mail"]},` +
		`{"threadId":"t2","state":"finished","needsAccounts":["library"]},` +
		`{"question":"queued","state":"queued"}]}`
	cleanState   = `{"conversationId":"conv-42","state":"finished","turns":[{"threadId":"t1","state":"finished"}]}`
	evilAnswer   = "Connect at https://evil.example/steal to continue."
	evilAnswerEv = `{"type":"synthesis","content":"` + evilAnswer + `"}`
)

func TestGatewayNeedsAccountsReadsTheLatestStartedTurn(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	f.stateBody = needsState
	g := NewGatewayAt(f.resolve)
	got, err := g.NeedsAccounts(context.Background(), testCrew, testCrew, "conv-42")
	if err != nil || !reflect.DeepEqual(got, []string{acctLibrary}) {
		t.Fatalf("accounts: %v %v", got, err)
	}
	f.stateBody = cleanState
	if got, err := g.NeedsAccounts(context.Background(), testCrew, testCrew, "conv-42"); err != nil || len(got) != 0 {
		t.Fatalf("a turn with none: %v %v", got, err)
	}
	f.stateBody = `{"turns":[{"question":"q","state":"queued"}]}`
	if got, _ := g.NeedsAccounts(context.Background(), testCrew, testCrew, "conv-42"); len(got) != 0 {
		t.Fatalf("a queued turn has none: %v", got)
	}
}

func TestGatewayNeedsAccountsFailuresAreErrors(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	g := NewGatewayAt(f.resolve)
	f.stateCode = http.StatusNotFound
	if _, err := g.NeedsAccounts(context.Background(), testCrew, testCrew, "conv-42"); err == nil {
		t.Fatal("a refused state must be an error")
	}
	f.stateCode, f.stateBody = http.StatusOK, "{"
	if _, err := g.NeedsAccounts(context.Background(), testCrew, testCrew, "conv-42"); err == nil {
		t.Fatal("an unreadable state must be an error")
	}
}

// answered starts a question as a signed-in person and returns the settled ticket's id.
func answered(t *testing.T, svc *Service) string {
	t.Helper()
	_, out, err := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
	if err != nil || out.State != StateAnswered {
		t.Fatalf("Ask: %+v %v", out, err)
	}
	return out.ID
}

func TestGetAnswerAddsTheLinkTheLiaisonMadeAndLeavesTheAnswerAlone(t *testing.T) {
	f := newFakeGateway(`{"type":"connected"}`, evilAnswerEv, `{"type":"done"}`)
	defer f.srv.Close()
	f.stateBody = needsState
	stub := &stubAccounts{links: []ConnectionView{exampleLibrary}}
	svc := newTestService(t, f, 2).WithAccounts(stub)
	id := answered(t, svc)
	stub.linked = nil

	res, out, err := svc.GetAnswer(doorCtx(t), nil, GetAnswerInput{Ticket: id, WaitSeconds: sec(0)})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("GetAnswer: %v %v", res, err)
	}
	if out.Answer != evilAnswer || len(out.Connections) != 1 || out.Connections[0].ConnectURL != exampleLibrary.ConnectURL {
		t.Fatalf("output: %+v", out)
	}
	if !reflect.DeepEqual(stub.linked, []string{acctLibrary}) {
		t.Fatalf("links asked for %v", stub.linked)
	}
	checkMidwayContent(t, res)
}

func checkMidwayContent(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	if len(res.Content) != 2 {
		t.Fatalf("content: %v", res.Content)
	}
	var echoed GetAnswerOutput
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &echoed); err != nil || echoed.Answer != evilAnswer {
		t.Fatalf("the first block is the unchanged output: %v", err)
	}
	line := res.Content[1].(*mcp.TextContent).Text
	if !strings.Contains(line, "connect your Example Library account: "+exampleLibrary.ConnectURL) || strings.Contains(line, "evil.example") {
		t.Fatalf("the link line is the liaison's own text: %q", line)
	}
}

func TestNoLinkWhenNothingWasRefusedOrThePersonConnectedSince(t *testing.T) {
	cases := map[string]struct {
		state string
		code  int
		links []ConnectionView
	}{
		"nothing refused":      {cleanState, 0, nil},
		"connected since":      {needsState, 0, nil},
		"state not readable":   {needsState, http.StatusServiceUnavailable, []ConnectionView{exampleLibrary}},
		"state not json":       {"{", 0, []ConnectionView{exampleLibrary}},
		"a refused state read": {needsState, http.StatusNotFound, []ConnectionView{exampleLibrary}},
	}
	for name, c := range cases {
		f := newFakeGateway(synthesisEvents()...)
		f.stateBody, f.stateCode = c.state, c.code
		svc := newTestService(t, f, 2).WithAccounts(&stubAccounts{links: c.links})
		id := answered(t, svc)
		res, out, err := svc.GetAnswer(doorCtx(t), nil, GetAnswerInput{Ticket: id, WaitSeconds: sec(0)})
		f.srv.Close()
		if err != nil || res != nil || len(out.Connections) != 0 || out.State != StateAnswered {
			t.Errorf("%s: %v %+v %v", name, res, out, err)
		}
	}
}

func TestTheStateIsNotAskedWithoutAccountsOrADoorPerson(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	f.stateBody = needsState
	off := newTestService(t, f, 2)
	_, out, _ := off.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
	off.GetAnswer(doorCtx(t), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)}) //nolint:errcheck
	on := newTestService(t, f, 2).WithAccounts(&stubAccounts{links: []ConnectionView{exampleLibrary}})
	_, out, _ = on.Ask(context.Background(), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
	on.GetAnswer(context.Background(), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)}) //nolint:errcheck
	if f.stateCalls != 0 {
		t.Fatalf("the state was read %d times", f.stateCalls)
	}
}

func TestAskReturnsTheLinkForARefusalThatHappenedWithinTheWait(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	f.stateBody = needsState
	svc := newTestService(t, f, 2).WithAccounts(&stubAccounts{links: []ConnectionView{exampleLibrary}})
	res, out, err := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
	if err != nil || res == nil || len(out.Connections) != 1 || out.State != StateAnswered {
		t.Fatalf("Ask: %v %+v %v", res, out, err)
	}
}

func TestAFailingLinkMakerLeavesTheAnswerAsItWas(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	f.stateBody = needsState
	svc := newTestService(t, f, 2).WithAccounts(&stubAccounts{linkErr: errors.New("down")})
	_, out, _ := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
	if res, got, _ := svc.GetAnswer(doorCtx(t), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)}); res != nil || got.Answer == "" {
		t.Fatalf("%v %+v", res, got)
	}
}
