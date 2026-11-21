package liaison

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	canaryToken = "CANARY-TOKEN-1"
	testService = "oreilly"
)

var hashKey = []byte("install-hash-key-for-tests")

// personService is a service that accepts every bearer token it is sent, and counts calls.
type personService struct {
	srv   *httptest.Server
	calls atomic.Int32
}

func newPersonService(t *testing.T) *personService {
	t.Helper()
	p := &personService{}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		if strings.Contains(r.Header.Get("Authorization"), "bad") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// personToken is a token shaped as the service issues them, naming sub.
func personToken(sub string, expires time.Time, extra string) string {
	raw, _ := json.Marshal(map[string]any{
		"upstream_claims": map[string]any{"sub": sub}, "exp": expires.Unix(), "x": extra,
	})
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString(raw) + ".c2ln"
}

// personAuth accepts Kubernetes tokens first and then a person's own token the service accepts,
// the chain the liaison builds from the same ACCESS_* settings as the gateway.
func personAuth(t *testing.T, p *personService, allowAnonymous bool) *identity.Authenticator {
	t.Helper()
	verifier, err := identity.NewServiceTokenVerifier(identity.ServiceTokenConfig{
		Name: testService, VerifyURL: p.srv.URL, SubjectClaim: "upstream_claims.sub", PassToTools: true,
	}, hashKey, p.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		ok := tr.Spec.Token == tokenAlice
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: ok, User: authenticationv1.UserInfo{Username: "alice"}}}, nil
	})
	return identity.NewAuthenticator(identity.Hasher{Key: hashKey}, allowAnonymous,
		identity.TokenReviewVerifier{Reviewer: identity.Reviewer{Client: c}}, verifier)
}

func personDoor(t *testing.T, f *fakeGateway, auth *identity.Authenticator) (*Service, *httptest.Server) {
	t.Helper()
	svc := newTestService(t, f, 4)
	srv := httptest.NewServer(NewHandlerWithFront(svc, "", auth, frontStub()))
	t.Cleanup(srv.Close)
	return svc, srv
}

func TestAServiceTokenCallerIsAcceptedAndOwnsItsTicket(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	p := newPersonService(t)
	svc, srv := personDoor(t, f, personAuth(t, p, false))
	ctx := context.Background()
	exp := time.Now().Add(15 * time.Minute)
	ann, bea := personToken("acct-ann", exp, ""), personToken("acct-bea", exp, "")

	session := connectSession(ctx, t, srv.URL+"/mcp", ann)
	out := callTool(ctx, t, session, "ask", map[string]any{argCrew: testCrew, argQuestion: testQuestion, "waitSeconds": 5})
	var asked TicketView
	if err := json.Unmarshal([]byte(out), &asked); err != nil {
		t.Fatal(err)
	}
	ticket, ok := svc.tickets.Get(asked.ID)
	if !ok || ticket.Owner == "" || strings.Contains(ticket.Owner, "acct-ann") {
		t.Fatalf("the owner should be the hashed principal id, got %q", ticket.Owner)
	}
	// The gateway got the person's token to decide for itself.
	if got := f.authorizations()[0]; got != "Bearer "+ann {
		t.Errorf("gateway authorization %q", got)
	}
	// Another person cannot read the ticket.
	other := connectSession(ctx, t, srv.URL+"/mcp", bea)
	res, err := other.CallTool(ctx, &mcp.CallToolParams{Name: "get_answer", Arguments: map[string]any{"ticket": asked.ID}})
	if err != nil || !res.IsError {
		t.Fatalf("another person read the ticket: %v %v", err, res)
	}
}

func TestAKubernetesTokenNeverGoesToTheService(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	p := newPersonService(t)
	_, srv := personDoor(t, f, personAuth(t, p, false))
	ctx := context.Background()
	session := connectSession(ctx, t, srv.URL+"/mcp", tokenAlice)
	callTool(ctx, t, session, "ask", map[string]any{argCrew: testCrew, argQuestion: testQuestion, "waitSeconds": 5})
	if n := p.calls.Load(); n != 0 {
		t.Fatalf("the Kubernetes token reached the service %d times", n)
	}
	if got := f.authorizations()[0]; got != "Bearer "+tokenAlice {
		t.Errorf("gateway authorization %q", got)
	}
}

func TestUnauthenticatedAndRefusedCallersGetTheChallenge(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	p := newPersonService(t)
	_, srv := personDoor(t, f, personAuth(t, p, false))
	want := `Bearer realm="kubemoot-crew-liaison", resource_metadata="` + metadataURL + `"`
	if got := challengeOf(t, srv.URL); got != want {
		t.Errorf("no token: challenge %q", got)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader("{}"))
	forged := strings.TrimSuffix(personToken("bad", time.Now().Add(time.Hour), ""), "c2ln") + "bad"
	req.Header.Set("Authorization", "Bearer "+forged)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != want {
		t.Errorf("a token the service refuses: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

// personCtx is the context the door builds for a person verified by a service.
func personCtx(sub, token string) context.Context {
	id := &identity.Identity{Issuer: testService, Subject: sub, Credential: identity.NewCredential(token)}
	hasher := identity.Hasher{Key: hashKey}
	caller := identity.Caller{
		Principal: bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: testService, ID: hasher.ID(testService, sub)},
		Identity:  id,
	}
	return withCredential(identity.WithCaller(context.Background(), caller), token)
}

func pendingService(t *testing.T) (*Service, *fakeGateway) {
	t.Helper()
	f := newFakeGateway(`{"type":"connected"}`)
	f.hold = make(chan struct{})
	t.Cleanup(func() { close(f.hold); f.srv.Close() })
	return newTestService(t, f, 4), f
}

func TestRenewalIsSentOnlyWhenTheTokenChanged(t *testing.T) {
	svc, f := pendingService(t)
	first, second := personToken("ann", time.Now().Add(time.Minute), "1"), personToken("ann", time.Now().Add(time.Hour), "2")
	_, out, _ := svc.Ask(personCtx("ann", first), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})
	poll := func(token string) {
		_, _, _ = svc.GetAnswer(personCtx("ann", token), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
	}
	poll(first)
	if n := len(f.renewed()); n != 0 {
		t.Fatalf("the token the ticket started with was sent again: %d", n)
	}
	poll(second)
	poll(second)
	got := f.renewed()
	if len(got) != 1 || got[0] != "Bearer "+second {
		t.Fatalf("renewals %v", got)
	}
	if path := f.renewalPath[0]; !strings.HasSuffix(path, "/conv-42/credential") {
		t.Errorf("renewal path %q", path)
	}
}

func TestRenewalFailureIsRetriedOnTheNextPoll(t *testing.T) {
	svc, f := pendingService(t)
	first, second := personToken("ann", time.Now().Add(time.Minute), "1"), personToken("ann", time.Now().Add(time.Hour), "2")
	_, out, _ := svc.Ask(personCtx("ann", first), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})
	f.mu.Lock()
	f.renewCode = http.StatusServiceUnavailable
	f.mu.Unlock()
	_, _, _ = svc.GetAnswer(personCtx("ann", second), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
	f.mu.Lock()
	f.renewCode = http.StatusOK
	f.mu.Unlock()
	_, _, _ = svc.GetAnswer(personCtx("ann", second), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
	_, _, _ = svc.GetAnswer(personCtx("ann", second), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
	if n := len(f.renewed()); n != 2 {
		t.Fatalf("want one failed and one accepted send, got %d", n)
	}
}

func TestRenewalIsForTheOwnerAndForServiceTokensOnly(t *testing.T) {
	svc, f := pendingService(t)
	first := personToken("ann", time.Now().Add(time.Minute), "1")
	_, out, _ := svc.Ask(personCtx("ann", first), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})

	// Another person with a different token: the ticket is unknown to them, nothing is sent.
	res, _, _ := svc.GetAnswer(personCtx("bea", personToken("bea", time.Now().Add(time.Hour), "")), nil,
		GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
	if res == nil || !res.IsError {
		t.Fatal("a person who is not the owner read the ticket")
	}
	// A Kubernetes caller has no credential to renew.
	k8s := identity.Caller{
		Principal: bus.Principal{Kind: bus.PrincipalKindKubernetes, ID: "k"}, Identity: &identity.Identity{Issuer: identity.IssuerKubernetes},
	}
	ctx := withCredential(identity.WithCaller(context.Background(), k8s), "k8s-token")
	svc.renew(ctx, mustTicket(t, svc, out.ID))
	if n := len(f.renewed()); n != 0 {
		t.Fatalf("renewals for a non-owner or a Kubernetes token: %v", f.renewed())
	}
}

func TestNoRenewalForASettledTicket(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 4)
	first := personToken("ann", time.Now().Add(time.Minute), "1")
	_, out, _ := svc.Ask(personCtx("ann", first), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
	if out.State != StateAnswered {
		t.Fatalf("not answered: %+v", out)
	}
	_, _, _ = svc.GetAnswer(personCtx("ann", personToken("ann", time.Now().Add(time.Hour), "2")), nil,
		GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
	if n := len(f.renewed()); n != 0 {
		t.Fatalf("renewed a settled ticket: %d", n)
	}
}

func mustTicket(t *testing.T, svc *Service, id string) *Ticket {
	t.Helper()
	tk, ok := svc.tickets.Get(id)
	if !ok {
		t.Fatal("no ticket")
	}
	return tk
}

func TestTheFollowerHoldsNoTokenOnceTheStreamIsOpen(t *testing.T) {
	f := newFakeGateway(`{"type":"connected"}`, `{"type":"synthesis","content":"x"}`)
	defer f.srv.Close()
	h := newHandoff("the-token")
	if !h.held() {
		t.Fatal("the handoff starts holding the token")
	}
	var heldInStream bool
	err := NewGatewayAt(f.resolve).Follow(context.Background(), testCrew, testCrew, "conv-42", h, func(Event) bool {
		heldInStream = h.held()
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if heldInStream || h.held() || h.take() != "" {
		t.Fatal("the follower still holds the token after the stream opened")
	}
	if got := f.authorizations(); len(got) != 1 || got[0] != "Bearer the-token" {
		t.Fatalf("the stream was not opened with the caller's token: %v", got)
	}
}

func TestAStartedTicketFollowerLeavesNoTokenBehind(t *testing.T) {
	svc, f := pendingService(t)
	f.opened = make(chan struct{}, 1)
	token := personToken("ann", time.Now().Add(time.Minute), "1")
	_, out, _ := svc.Ask(personCtx("ann", token), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})
	select {
	case <-f.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never opened")
	}
	if got := f.authorizations(); len(got) != 2 || got[1] != "Bearer "+token {
		t.Fatalf("authorizations %v", got)
	}
	// The ticket keeps a fingerprint of the token and not the token.
	tk := mustTicket(t, svc, out.ID)
	if !tk.sentCredential(svc.tickets.fingerprints.of(token)) {
		t.Error("the start token should be remembered as sent")
	}
}

func TestNoTokenIsLogged(t *testing.T) {
	captureLogs()
	svc, f := pendingService(t)
	f.mu.Lock()
	f.renewCode = http.StatusInternalServerError
	f.mu.Unlock()
	start := personToken("ann", time.Now().Add(time.Minute), "start-"+canaryToken)
	fresh := personToken("ann", time.Now().Add(time.Hour), "fresh-"+canaryToken)
	_, out, _ := svc.Ask(personCtx("ann", start), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})
	_, _, _ = svc.GetAnswer(personCtx("ann", fresh), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
	if !strings.Contains(capturedLogs(), "could not renew") {
		t.Fatalf("the failed renewal was not logged, so this proves nothing:\n%s", capturedLogs())
	}
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, secret := range []string{start, fresh, canaryToken, b64(canaryToken), strings.Split(fresh, ".")[1]} {
		if strings.Contains(capturedLogs(), secret) {
			t.Errorf("the log carries part of a token: %q", secret)
		}
	}
}

func TestConcurrentPollsSendOneFreshTokenOnce(t *testing.T) {
	svc, f := pendingService(t)
	first, second := personToken("ann", time.Now().Add(time.Minute), "1"), personToken("ann", time.Now().Add(time.Hour), "2")
	_, out, _ := svc.Ask(personCtx("ann", first), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = svc.GetAnswer(personCtx("ann", second), nil, GetAnswerInput{Ticket: out.ID, WaitSeconds: sec(0)})
		}()
	}
	wg.Wait()
	if n := len(f.renewed()); n != 1 {
		t.Fatalf("one fresh token was sent %d times", n)
	}
}

func TestRenewReportsAnUnreachableGatewayAndARefusal(t *testing.T) {
	f := newFakeGateway()
	g := NewGatewayAt(f.resolve)
	f.mu.Lock()
	f.renewCode = http.StatusForbidden
	f.mu.Unlock()
	err := g.Renew(context.Background(), testCrew, testCrew, "conv-42", canaryToken)
	if err == nil || strings.Contains(err.Error(), canaryToken) {
		t.Fatalf("a refusal should be an error without the token: %v", err)
	}
	f.srv.Close()
	err = g.Renew(context.Background(), testCrew, testCrew, "conv-42", canaryToken)
	if err == nil || strings.Contains(err.Error(), canaryToken) {
		t.Fatalf("an unreachable gateway should be an error without the token: %v", err)
	}
}

func TestHandoffAndFingerprints(t *testing.T) {
	var none *handoff
	if none.take() != "" || none.held() {
		t.Error("a nil handoff holds nothing")
	}
	h := newHandoff("t")
	if h.take() != "t" || h.take() != "" {
		t.Error("the token is given once")
	}
	a, b := newFingerprinter(), newFingerprinter()
	again := a.of("x")
	if a.of("x") != again || a.of("x") == a.of("y") || a.of("x") == b.of("x") {
		t.Error("a fingerprint is stable for a token and key, and differs by token and by key")
	}
}
