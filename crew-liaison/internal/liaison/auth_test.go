package liaison

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	argCrew     = "crew"
	argQuestion = "question"
	saToken     = "sa-token"
	tokenAlice  = "alice-token"
	tokenBob    = "bob-token"
	sharedToken = "s3cret"
)

// clusterAuth accepts the Kubernetes tokens of alice and bob.
func clusterAuth(allowAnonymous bool) *identity.Authenticator {
	users := map[string]string{tokenAlice: "alice", tokenBob: "bob"}
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		u, ok := users[tr.Spec.Token]
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: ok, User: authenticationv1.UserInfo{Username: u}}}, nil
	})
	return identity.NewAuthenticator(identity.Hasher{}, allowAnonymous,
		identity.TokenReviewVerifier{Reviewer: identity.Reviewer{Client: c}})
}

func status(t *testing.T, url, authorization string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestDoorWithoutAnAuthenticatorIsTheStaticGateAsBefore(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandlerWithAuth(newTestService(t, f, 1), sharedToken, identity.AnonymousAuthenticator()))
	defer srv.Close()
	// A Kubernetes token is not recognized: only the shared token passes.
	if got := status(t, srv.URL+"/mcp", "Bearer "+tokenAlice); got != http.StatusUnauthorized {
		t.Fatalf("status %d", got)
	}
}

func TestDoorChecksKubernetesTokens(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandlerWithAuth(newTestService(t, f, 1), sharedToken, clusterAuth(false)))
	defer srv.Close()
	for name, tc := range map[string]struct {
		authorization string
		refused       bool
	}{
		"missing":       {"", true},
		"invalid":       {"Bearer nope", true},
		"wrong scheme":  {"Basic " + tokenAlice, true},
		"kubernetes":    {"Bearer " + tokenAlice, false},
		"shared secret": {"Bearer " + sharedToken, false},
	} {
		got := status(t, srv.URL+"/mcp", tc.authorization)
		if tc.refused != (got == http.StatusUnauthorized) {
			t.Errorf("%s: status %d", name, got)
		}
	}
	if got := status(t, srv.URL+"/health", ""); got != http.StatusOK {
		t.Errorf("health status %d", got)
	}
}

func TestDoorAcceptsAnAnonymousCallerOnlyWhenAllowed(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandlerWithAuth(newTestService(t, f, 1), "", clusterAuth(true)))
	defer srv.Close()
	if got := status(t, srv.URL+"/mcp", ""); got == http.StatusUnauthorized {
		t.Error("an anonymous caller must pass when allowed")
	}
	if got := status(t, srv.URL+"/mcp", "Bearer nope"); got != http.StatusUnauthorized {
		t.Errorf("a bad token is never anonymous: %d", got)
	}
}

// The caller's token reaches the gateway on the question and on the stream, the shared
// token never does, and a ticket belongs to the caller who asked.
func TestTheCallersTokenTravelsThroughTheMCPDoorAndTicketsBelongToTheirCaller(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 4)
	srv := httptest.NewServer(NewHandlerWithAuth(svc, sharedToken, clusterAuth(false)))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	alice := connectSession(ctx, t, srv.URL+"/mcp", tokenAlice)
	var asked AskOutput
	out := callTool(ctx, t, alice, "ask", map[string]any{argCrew: testCrew, argQuestion: testQuestion, argWaitSeconds: 0})
	if err := json.Unmarshal([]byte(out), &asked); err != nil || asked.ID == "" {
		t.Fatalf("%v %s", err, out)
	}
	settle(t, svc, asked.ID)
	for i, got := range f.authorizations() {
		if got != "Bearer "+tokenAlice {
			t.Errorf("gateway call %d carried %q, want alice's token", i, got)
		}
	}
	if len(f.authorizations()) < 2 {
		t.Fatalf("want the question and the stream, got %v", f.authorizations())
	}

	bob := connectSession(ctx, t, srv.URL+"/mcp", tokenBob)
	if res := toolText(ctx, t, bob, "get_answer", map[string]any{argTicket: asked.ID}); !strings.Contains(res, "unknown or expired ticket") {
		t.Fatalf("bob read alice's ticket: %s", res)
	}
	if res := callTool(ctx, t, alice, "get_answer", map[string]any{argTicket: asked.ID}); !strings.Contains(res, "Paris") {
		t.Fatalf("alice cannot read her own ticket: %s", res)
	}
}

func TestTheSharedTokenIsNeverForwardedAndActsAnonymously(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 4)
	srv := httptest.NewServer(NewHandlerWithAuth(svc, sharedToken, clusterAuth(false)))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connectSession(ctx, t, srv.URL+"/mcp", sharedToken)
	var asked AskOutput
	_ = json.Unmarshal([]byte(callTool(ctx, t, session, "ask",
		map[string]any{argCrew: testCrew, argQuestion: testQuestion, argWaitSeconds: 0})), &asked)
	settle(t, svc, asked.ID)
	for _, got := range f.authorizations() {
		if got != "" {
			t.Errorf("the shared token was forwarded: %q", got)
		}
	}
}

func TestRejoinNeedsTheSameOwner(t *testing.T) {
	s := NewStore(time.Hour)
	a := s.Create(testCrew, testCrew, "c1", "what?", "owner-a")
	if _, ok := s.Rejoin(a.ID, testCrew, testCrew, "what?", "owner-b"); ok {
		t.Fatal("another principal rejoined the ticket")
	}
	if _, ok := s.Rejoin(a.ID, testCrew, testCrew, "what?", ""); ok {
		t.Fatal("an anonymous caller rejoined a principal's ticket")
	}
	if got, ok := s.Rejoin(a.ID, testCrew, testCrew, "what?", "owner-a"); !ok || got != a {
		t.Fatal("the owner must rejoin")
	}
}

// toolText calls a tool and returns its text whether or not it reported an error.
func toolText(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return textOf(res)
}

// Anonymous never loosens a configured shared token: with both set, a request that
// presents nothing is refused.
func TestAnAllowedAnonymousCallerStillNeedsTheSharedTokenWhenOneIsSet(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandlerWithAuth(newTestService(t, f, 1), sharedToken, clusterAuth(true)))
	defer srv.Close()
	if got := status(t, srv.URL+"/mcp", ""); got != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", got)
	}
	if got := status(t, srv.URL+"/mcp", "Bearer "+tokenAlice); got == http.StatusUnauthorized {
		t.Fatal("a valid Kubernetes token must pass")
	}
}
