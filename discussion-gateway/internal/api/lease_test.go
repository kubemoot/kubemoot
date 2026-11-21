package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/grants"
)

const (
	brokerSA      = "system:serviceaccount:ns:mcp-gateway"
	saGood        = "sa-good"
	saOtherSA     = "sa-other"
	saNoAudience  = "sa-api-audience-only"
	saLyingServer = "sa-lying"
	leasePath     = "/internal/v1/lease"
)

// serviceAccounts is a fake API server for the lease caller's token: each token is a
// ServiceAccount valid for the audiences listed. A review is authenticated only for an
// audience the token has, as the real server decides; saLyingServer is answered as
// authenticated with no audience at all, to show the gateway checks the answer again.
func serviceAccounts() *fake.Clientset {
	type account struct {
		user      string
		audiences []string
	}
	tokens := map[string]account{
		saGood:        {brokerSA, []string{BrokerAudience}},
		saOtherSA:     {"system:serviceaccount:ns:someone-else", []string{BrokerAudience}},
		saNoAudience:  {brokerSA, []string{"https://kubernetes.default.svc"}},
		saLyingServer: {brokerSA, nil},
	}
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		acc, known := tokens[tr.Spec.Token]
		if tr.Spec.Token == "reviewer-down" {
			return true, nil, errors.New("api server unreachable")
		}
		var granted []string
		for _, want := range tr.Spec.Audiences {
			for _, have := range acc.audiences {
				if want == have {
					granted = append(granted, want)
				}
			}
		}
		ok := known && (len(granted) > 0 || tr.Spec.Token == saLyingServer)
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: ok, User: authenticationv1.UserInfo{Username: acc.user}, Audiences: granted}}, nil
	})
	return c
}

type leaseRig struct {
	srv   *httptest.Server
	store *grants.Memory
	now   *time.Time
}

func newLeaseRig(t *testing.T) leaseRig {
	t.Helper()
	now := time.Now()
	store := grants.NewMemory(0, func() time.Time { return now })
	rv := identity.Reviewer{Client: serviceAccounts(), Audiences: []string{BrokerAudience}}
	mux := http.NewServeMux()
	NewLeaseServer(store, rv, brokerSA).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return leaseRig{srv: srv, store: store, now: &now}
}

func (l leaseRig) put(t *testing.T, conv, principal, token string) {
	t.Helper()
	owner := bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: verifierName, ID: principal}
	if err := l.store.Put(conv, grants.New(owner, verifierName, token, l.now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
}

func leaseBody(conv, principal string) string {
	b, _ := json.Marshal(leaseRequest{ConversationID: conv, PrincipalID: principal, Verifier: verifierName})
	return string(b)
}

func (l leaseRig) lease(t *testing.T, saToken, body string) (*http.Response, string) {
	t.Helper()
	resp := do(t, http.MethodPost, l.srv.URL+leasePath, saToken, body)
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestALeaseAnswersTheTokenAndWhenItEndsAndIsNotCacheable(t *testing.T) {
	l := newLeaseRig(t)
	l.put(t, "c1", "p1", "token-of-p1")
	resp, body := l.lease(t, saGood, leaseBody("c1", "p1"))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var got leaseResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Token != "token-of-p1" {
		t.Fatalf("body %s %v", body, err)
	}
	if end, err := time.Parse(time.RFC3339, got.ExpiresAt); err != nil || end.Before(l.now.Add(time.Second)) {
		t.Fatalf("expiresAt %q", got.ExpiresAt)
	}
}

func TestALeaseIsRefusedUnlessTheTurnAndPrincipalMatch(t *testing.T) {
	l := newLeaseRig(t)
	l.put(t, "c1", "p1", "token-of-p1")
	l.put(t, "old", "p1", "token-old")
	cases := []struct {
		name, body string
		want       int
		advance    time.Duration
	}{
		{"no grant", leaseBody("nobody", "p1"), http.StatusNotFound, 0},
		{"another principal", leaseBody("c1", "p2"), http.StatusForbidden, 0},
		{"another verifier", `{"conversationId":"c1","principalId":"p1","verifier":"other"}`, http.StatusForbidden, 0},
		{"expired", leaseBody("old", "p1"), http.StatusGone, 2 * time.Minute},
		{"expired, wrong principal", leaseBody("old", "p2"), http.StatusForbidden, 0},
		{"missing principal", `{"conversationId":"c1","verifier":"oreilly"}`, http.StatusBadRequest, 0},
		{"not JSON", `nope`, http.StatusBadRequest, 0},
		{"unknown field", `{"conversationId":"c1","principalId":"p1","verifier":"oreilly","x":1}`, http.StatusBadRequest, 0},
		{"bad conversation id", leaseBody("a.b", "p1"), http.StatusBadRequest, 0},
	}
	for _, c := range cases {
		*l.now = l.now.Add(c.advance)
		resp, body := l.lease(t, saGood, c.body)
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, resp.StatusCode, c.want)
		}
		if strings.Contains(body, "token-of") || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: a refusal must carry no token and be no-store: %s", c.name, body)
		}
	}
}

func TestALeaseIsRefusedToAnyCallerButTheNamedServiceAccountWithTheBrokerAudience(t *testing.T) {
	l := newLeaseRig(t)
	l.put(t, "c1", "p1", "token-of-p1")
	cases := []struct {
		name, token string
		want        int
	}{
		{"a different ServiceAccount", saOtherSA, http.StatusForbidden},
		{"the right ServiceAccount, a token without the audience", saNoAudience, http.StatusUnauthorized},
		{"a server that answers authenticated with no audience", saLyingServer, http.StatusUnauthorized},
		{"no token", "", http.StatusUnauthorized},
		{"an unknown token", garbageToken, http.StatusUnauthorized},
		{"the API server cannot be asked", "reviewer-down", http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		resp, body := l.lease(t, c.token, leaseBody("c1", "p1"))
		if resp.StatusCode != c.want || strings.Contains(body, "token-of-p1") {
			t.Errorf("%s: %d %s, want %d and no token", c.name, resp.StatusCode, body, c.want)
		}
	}
}

func TestALeaseServerWithNoNamedServiceAccountServesNoOne(t *testing.T) {
	store := grants.NewMemory(0, nil)
	rv := identity.Reviewer{Client: serviceAccounts(), Audiences: []string{BrokerAudience}}
	mux := http.NewServeMux()
	NewLeaseServer(store, rv, "").RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if resp := do(t, http.MethodPost, srv.URL+leasePath, saGood, leaseBody("c1", "p1")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no subject configured: %d, want 403", resp.StatusCode)
	}
}

func TestTwoConversationsLeaseTheirOwnTokens(t *testing.T) {
	l := newLeaseRig(t)
	l.put(t, "c1", "p1", "token-of-p1")
	l.put(t, "c2", "p2", "token-of-p2")
	for _, c := range [][3]string{{"c1", "p1", "token-of-p1"}, {"c2", "p2", "token-of-p2"}, {"c1", "p2", ""}, {"c2", "p1", ""}} {
		_, body := l.lease(t, saGood, leaseBody(c[0], c[1]))
		if c[2] != "" && !strings.Contains(body, c[2]) {
			t.Errorf("lease %s as %s: %s", c[0], c[1], body)
		}
		if c[2] == "" && strings.Contains(body, "token-of") {
			t.Errorf("lease %s as %s leaked a token: %s", c[0], c[1], body)
		}
	}
}

func TestTheLeaseIsNotOnThePublicMux(t *testing.T) {
	r := newGrantRig(t, true, 0)
	if resp := do(t, http.MethodPost, r.srv.URL+leasePath, saGood, leaseBody("c1", "p1")); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the public mux answers the lease route: %d, want 404", resp.StatusCode)
	}
}

func TestALeaseOfAGrantTheGatewayHeldEndToEndAndTheLogsStayClean(t *testing.T) {
	logs := captureLogs(t)
	r := newGrantRig(t, true, 0)
	person := personTokenUntil("person-1", time.Now().Add(time.Hour))
	r.ask(t, person, grantConv)
	principal := identity.Hasher{Key: []byte(hashKey)}.ID(verifierName, "person-1")

	rv := identity.Reviewer{Client: serviceAccounts(), Audiences: []string{BrokerAudience}}
	mux := http.NewServeMux()
	NewLeaseServer(r.store, rv, brokerSA).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := do(t, http.MethodPost, srv.URL+leasePath, saGood, leaseBody(grantConv, principal))
	var got leaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || got.Token != person {
		t.Fatalf("lease %d %v", resp.StatusCode, err)
	}
	do(t, http.MethodPost, srv.URL+leasePath, saGood, leaseBody(grantConv, "someone-else"))
	if strings.Contains(logs(), subscriberCanary) || strings.Contains(logs(), saGood) {
		t.Fatalf("a token is in the logs: %s", logs())
	}
}
