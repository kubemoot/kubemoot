package liaison

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// hmacSealer is a StateSealer that signs, not encrypts, which is enough to tell a value from
// another purpose or a changed one.
type hmacSealer struct{ key []byte }

func (h hmacSealer) mac(purpose, payload string) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte(purpose + "\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (h hmacSealer) Seal(purpose string, v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + h.mac(purpose, payload), nil
}

func (h hmacSealer) Open(purpose, blob string, v any) error {
	payload, sig, ok := strings.Cut(blob, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(h.mac(purpose, payload))) {
		return errors.New("bad")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

const (
	revision2026 = "2026-07-28"
	toolAsk      = "ask"
)

// callerWith is a tool request on revision 2026-07-28 with the client's capabilities and the
// state it echoes back, if any.
func callerWith(caps map[string]any, state string) *mcp.CallToolRequest {
	meta := mcp.Meta{mcp.MetaKeyProtocolVersion: revision2026, mcp.MetaKeyClientCapabilities: caps}
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: toolAsk, Meta: meta, RequestState: state}}
}

var urlCaps = map[string]any{"elicitation": map[string]any{"form": map[string]any{}, "url": map[string]any{}}}

func elicitService(t *testing.T, f *fakeGateway, stub *stubAccounts) *Service {
	t.Helper()
	return newTestService(t, f, 2).WithAccounts(stub).WithStateSealer(hmacSealer{key: []byte("k")})
}

func otherPersonCtx(t *testing.T) context.Context {
	t.Helper()
	signer, _ := hopKeyPair(t)
	return withAsserter(context.Background(), signer, &identity.Identity{Issuer: identity.IssuerKubernetes, Subject: "oidc:bob"})
}

func askAs(svc *Service, req *mcp.CallToolRequest, ctx context.Context) (*mcp.CallToolResult, AskOutput) {
	res, out, _ := svc.Ask(ctx, req, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
	return res, out
}

func TestAClientThatCanOpenLinksIsSentToTheConnectPage(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := elicitService(t, f, &stubAccounts{pending: []ConnectionView{exampleLibrary}})
	res, _ := askAs(svc, callerWith(urlCaps, ""), doorCtx(t))
	if res == nil || len(res.Content) != 0 || res.RequestState == "" || len(res.InputRequests) != 1 {
		t.Fatalf("result: %+v", res)
	}
	checkElicit(t, res)
	if f.started() != 0 {
		t.Error("nothing starts before the connection exists")
	}
}

func checkElicit(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	elicit, ok := res.InputRequests["connect_"+acctLibrary].(*mcp.ElicitParams)
	if !ok || elicit.Mode != "url" || elicit.URL != exampleLibrary.ConnectURL || elicit.ElicitationID == "" ||
		!strings.Contains(elicit.Message, "Example Library") {
		t.Fatalf("elicitation: %+v", res.InputRequests)
	}
}

func TestOtherClientsKeepTheLinkInTheResult(t *testing.T) {
	cases := map[string]struct {
		req    *mcp.CallToolRequest
		sealer bool
	}{
		"no capabilities":   {callerWith(map[string]any{}, ""), true},
		"form only":         {callerWith(map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}, ""), true},
		"an older revision": {&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "ask"}}, true},
		"no request":        {nil, true},
		"no state sealer":   {callerWith(urlCaps, ""), false},
	}
	for name, c := range cases {
		f := newFakeGateway(synthesisEvents()...)
		svc := newTestService(t, f, 2).WithAccounts(&stubAccounts{pending: []ConnectionView{exampleLibrary}})
		if c.sealer {
			svc.WithStateSealer(hmacSealer{key: []byte("k")})
		}
		res, out := askAs(svc, c.req, doorCtx(t))
		f.srv.Close()
		if res == nil || len(res.InputRequests) != 0 || out.State != StateNeedsConnection {
			t.Errorf("%s: %+v %+v", name, res, out)
			continue
		}
		checkLinkLine(t, res)
	}
}

// issuedState is the requestState a first ask returned.
func issuedState(t *testing.T, svc *Service) string {
	t.Helper()
	res, _ := askAs(svc, callerWith(urlCaps, ""), doorCtx(t))
	if res == nil || res.RequestState == "" {
		t.Fatalf("no state issued: %+v", res)
	}
	return res.RequestState
}

func TestTheRetryProceedsOnceTheAccountIsConnected(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	stub := &stubAccounts{pending: []ConnectionView{exampleLibrary}}
	svc := elicitService(t, f, stub)
	state := issuedState(t, svc)

	stub.pending = nil
	res, out := askAs(svc, callerWith(urlCaps, state), doorCtx(t))
	if res != nil || out.State != StateAnswered {
		t.Fatalf("the retry: %+v %+v", res, out)
	}
}

func TestTheRetryAsksAgainWhileTheAccountIsStillMissing(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := elicitService(t, f, &stubAccounts{pending: []ConnectionView{exampleLibrary}})
	state := issuedState(t, svc)
	res, _ := askAs(svc, callerWith(urlCaps, state), doorCtx(t))
	if res == nil || len(res.InputRequests) != 1 || res.RequestState == "" || res.RequestState == state {
		t.Fatalf("a fresh round: %+v", res)
	}
}

func TestAStateThatIsNotThisCallsIsRefusedAndStartsNothing(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	clock := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	stub := &stubAccounts{pending: []ConnectionView{exampleLibrary}}
	svc := elicitService(t, f, stub)
	svc.now = func() time.Time { return clock }
	state := issuedState(t, svc)
	stub.pending = nil

	for name, c := range map[string]struct {
		state    string
		question string
		advance  time.Duration
		ctx      context.Context
	}{
		"another question": {state, "a different question", 0, doorCtx(t)},
		"expired":          {state, testQuestion, stateLife + time.Second, doorCtx(t)},
		"tampered":         {state[:len(state)-3] + "AAA", testQuestion, 0, doorCtx(t)},
		"garbage":          {"not a state", testQuestion, 0, doorCtx(t)},
		"another person":   {state, testQuestion, 0, otherPersonCtx(t)},
	} {
		svc.now = func() time.Time { return clock.Add(c.advance) }
		res, _, _ := svc.Ask(c.ctx, callerWith(urlCaps, c.state), AskInput{Crew: testCrew, Question: c.question, WaitSeconds: sec(0)})
		if res == nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "ask again") {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if f.started() != 0 {
		t.Errorf("a refused retry started %d discussions", f.started())
	}
}

// TestTheWireAnswerIsInputRequired pins what a 2026-07-28 client receives.
func TestTheWireAnswerIsInputRequired(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := elicitService(t, f, &stubAccounts{pending: []ConnectionView{exampleLibrary}})
	handler := newMCPHandler(NewMCPServer(svc))
	signed := doorCtx(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(signed))
	}))
	defer srv.Close()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"crew":"` + testCrew +
		`","question":"` + testQuestion + `"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"url":{}}}}}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", revision2026)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", toolAsk)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var got struct {
		Result struct {
			ResultType    string                     `json:"resultType"`
			InputRequests map[string]json.RawMessage `json:"inputRequests"`
			RequestState  string                     `json:"requestState"`
			Content       []any                      `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	r := got.Result
	if r.ResultType != "input_required" || len(r.InputRequests) != 1 || r.RequestState == "" || len(r.Content) != 0 ||
		!strings.Contains(string(r.InputRequests["connect_"+acctLibrary]), `"mode":"url"`) {
		t.Fatalf("wire answer: %s", raw)
	}
}
