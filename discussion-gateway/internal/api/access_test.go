package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"github.com/nats-io/nats.go/jetstream"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
)

const (
	tokenAlice  = "alice-token"
	tokenBob    = "bob-token"
	tokenAdmin  = "admin-token"
	tokenNoRole = "norole-token"
	tokenReader = "reader-token"
	userAlice   = "alice"
	userBob     = "bob"
	userAdmin   = "admin"
	userNoRole  = "norole"
	userReader  = "reader"
	postPath    = "/api/v1/discussions/pilot"
)

// cluster is a fake API server: tokens map to users, and rbac maps "user verb resource[/sub]"
// to allowed. Every user but userNoRole may get crews; only userAdmin may read conversations.
func cluster() *fake.Clientset {
	users := map[string]string{tokenAlice: userAlice, tokenBob: userBob, tokenAdmin: userAdmin, tokenNoRole: userNoRole, tokenReader: userReader}
	rbac := map[string]bool{
		userAlice + " get crews": true, userBob + " get crews": true, userAdmin + " get crews": true,
		userAdmin + " get crews/conversations": true, userAdmin + " list crews/conversations": true,
		userAdmin + " update crews/conversations": true, userAdmin + " delete crews/conversations": true,
		userReader + " get crews": true, userReader + " get crews/conversations": true,
		userReader + " list crews/conversations": true,
	}
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		u, ok := users[tr.Spec.Token]
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: ok, User: authenticationv1.UserInfo{Username: u}, Audiences: tr.Spec.Audiences}}, nil
	})
	c.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		ra := sar.Spec.ResourceAttributes
		key := sar.Spec.User + " " + ra.Verb + " " + ra.Resource
		if ra.Subresource != "" {
			key += "/" + ra.Subresource
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: rbac[key]}}, nil
	})
	return c
}

// enforcingServer is a gateway that checks Kubernetes tokens against cluster().
func enforcingServer(t *testing.T, nc *natsclient.Client, allowAnonymous bool) *httptest.Server {
	t.Helper()
	rv := identity.Reviewer{Client: cluster()}
	auth := identity.NewAuthenticator(identity.Hasher{}, allowAnonymous, identity.TokenReviewVerifier{Reviewer: rv})
	mux := http.NewServeMux()
	NewHandler(nc, "ns").WithAccess(auth, rv).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// do sends a request with an optional bearer token and returns the status.
func do(t *testing.T, method, url, token, body string, header ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// queued returns the nth request (1-based) the gateway put on the request stream.
func queued(t *testing.T, js jetstream.JetStream, n uint64) bus.DiscussionRequest {
	t.Helper()
	stream, err := js.Stream(context.Background(), "KUBEMOOT_REQUEST")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := stream.GetMsg(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	var req bus.DiscussionRequest
	if err := json.Unmarshal(raw.Data, &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestAnUnconfiguredGatewayIgnoresTokensAndStampsNoPrincipal(t *testing.T) {
	nc, js := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	for _, token := range []string{"", tokenAlice, "garbage"} {
		if resp := do(t, http.MethodPost, srv.URL+postPath, token, `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
			t.Fatalf("token %q: status %d, want 200 as before", token, resp.StatusCode)
		}
	}
	stream, _ := js.Stream(context.Background(), "KUBEMOOT_REQUEST")
	raw, _ := stream.GetMsg(context.Background(), 1)
	if strings.Contains(string(raw.Data), "principal") {
		t.Fatalf("an unconfigured crew's request must be byte for byte as before: %s", raw.Data)
	}
}

func TestAnEnforcingGatewayRefusesMissingAndInvalidTokens(t *testing.T) {
	nc, _ := jetStreamServer(t)
	srv := enforcingServer(t, nc, false)
	for _, token := range []string{"", "garbage"} {
		resp := do(t, http.MethodPost, srv.URL+postPath, token, `{"message":"hi"}`)
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("token %q: status %d challenge %q", token, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}
	for _, path := range []string{"/health", "/ready"} {
		if resp := do(t, http.MethodGet, srv.URL+path, "", ""); resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("%s must not need a credential", path)
		}
	}
}

func TestAnEnforcingGatewayRefusesACallerWhoMayNotUseTheCrew(t *testing.T) {
	nc, _ := jetStreamServer(t)
	srv := enforcingServer(t, nc, false)
	if resp := do(t, http.MethodPost, srv.URL+postPath, tokenNoRole, `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403 from the SubjectAccessReview", resp.StatusCode)
	}
}

func TestAVerifiedCallerBecomesTheQueuedRequestsPrincipalAndABodyPrincipalIsIgnored(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	srv := enforcingServer(t, nc, false)
	body := `{"message":"hi","principal":{"kind":"kubernetes","id":"someone-else"}}`
	if resp := do(t, http.MethodPost, srv.URL+postPath, tokenAlice, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	p := queued(t, js, 1).Principal
	want := identity.Hasher{}.ID(identity.IssuerKubernetes, userAlice)
	if p == nil || p.Kind != bus.PrincipalKindKubernetes || p.ID != want || p.Issuer != identity.IssuerKubernetes {
		t.Fatalf("principal %+v, want id %s", p, want)
	}
	if strings.Contains(p.ID, userAlice) {
		t.Fatal("the subject must not travel")
	}
}

func TestAnonymousCallersAreAcceptedOnlyWhenAllowedAndStayAnonymous(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	srv := enforcingServer(t, nc, true)
	if resp := do(t, http.MethodPost, srv.URL+postPath, "", `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if p := queued(t, js, 1).Principal; p == nil || p.Kind != bus.PrincipalKindAnonymous || p.ID != "" {
		t.Fatalf("principal %+v", p)
	}
	// A bad token is still refused: it is never downgraded to anonymous.
	if resp := do(t, http.MethodPost, srv.URL+postPath, "garbage", `{"message":"hi"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// A token valid for the API server but bound to another audience is refused.
func TestAGatewayWithAudiencesRefusesATokenBoundToAnotherAudience(t *testing.T) {
	nc, _ := jetStreamServer(t)
	c := cluster()
	c.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: userAlice}, Audiences: []string{"api"}}}, nil
	})
	rv := identity.Reviewer{Client: c, Audiences: []string{"kubemoot-gateway"}}
	mux := http.NewServeMux()
	NewHandler(nc, "ns").WithAccess(identity.NewAuthenticator(identity.Hasher{}, false, identity.TokenReviewVerifier{Reviewer: rv}), rv).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if resp := do(t, http.MethodPost, srv.URL+postPath, tokenAlice, `{"message":"hi"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// A review that cannot be made refuses with 503 and no cause in the body, and never allows.
func TestAFailedReviewRefusesWithoutLeakingTheCause(t *testing.T) {
	for _, resource := range []string{"tokenreviews", "subjectaccessreviews"} {
		nc, _ := jetStreamServer(t)
		c := cluster()
		c.PrependReactor("create", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("dial tcp 10.1.2.3:443: refused")
		})
		rv := identity.Reviewer{Client: c}
		mux := http.NewServeMux()
		NewHandler(nc, "ns").WithAccess(identity.NewAuthenticator(identity.Hasher{}, true, identity.TokenReviewVerifier{Reviewer: rv}), rv).RegisterRoutes(mux)
		srv := httptest.NewServer(mux)
		resp := do(t, http.MethodPost, srv.URL+postPath, tokenAlice, `{"message":"hi"}`)
		body, _ := io.ReadAll(resp.Body)
		srv.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "10.1.2.3") {
			t.Errorf("%s: status %d body %s", resource, resp.StatusCode, body)
		}
	}
}

// Identity is never read from a header a client can set: a forwarded-user header with no
// credential is a caller with no credential.
func TestAnIdentityHeaderIsNotACredential(t *testing.T) {
	nc, _ := jetStreamServer(t)
	srv := enforcingServer(t, nc, false)
	resp := do(t, http.MethodPost, srv.URL+postPath, "", `{"message":"hi"}`,
		"X-Forwarded-User", userAlice, "Kubemoot-Principal", `{"kind":"kubernetes","id":"x"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

// createConversationsBucket makes the bucket owner records live in.
func createConversationsBucket(t *testing.T, js jetstream.JetStream) {
	t.Helper()
	if _, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: bus.ConversationsBucket}); err != nil {
		t.Fatal(err)
	}
}
