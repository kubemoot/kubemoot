package api

import (
	"encoding/base64"
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
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/grants"
	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
)

const (
	verifierName = "oreilly"
	garbageToken = "garbage"
	grantConv    = "c1"
	grantPath    = postPath + "/" + grantConv + "/credential"
)

// personTokenUntil is a token for subject that the service accepts until exp, whose signature
// is the canary. Two tokens for one subject differ when their exp differ.
func personTokenUntil(subject string, exp time.Time) string {
	payload, _ := json.Marshal(map[string]any{"upstream_claims": map[string]any{"sub": subject}, "exp": exp.Unix()})
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString(payload) + "." + subscriberCanary
}

// grantRig is a gateway that verifies Kubernetes tokens and people's tokens, keeps the tokens
// of people whose verifier passes them, and has a lease server over the same store.
type grantRig struct {
	srv   *httptest.Server
	store *grants.Memory
	js    jetstream.JetStream
	owned ownedSetup
}

func newGrantRig(t *testing.T, passToTools bool, maxGrants int) grantRig {
	t.Helper()
	nc, js := jetStreamServer(t)
	return newGrantRigOn(t, nc, js, passToTools, maxGrants)
}

func newGrantRigOn(t *testing.T, nc *natsclient.Client, js jetstream.JetStream, passToTools bool, maxGrants int) grantRig {
	t.Helper()
	owned := newOwnedSetupOn(t, nc, js, false)
	svc := newPersonService(t, http.StatusOK)
	raw := `[{"name":"oreilly","verifyURL":"` + svc.srv.URL + `","subjectClaim":"upstream_claims.sub","passToTools":` + boolText(passToTools) + `}]`
	cfgs, err := identity.ParseServiceTokens(raw)
	if err != nil {
		t.Fatal(err)
	}
	v, err := identity.NewServiceTokenVerifier(cfgs[0], []byte(hashKey), svc.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	rv := identity.Reviewer{Client: cluster()}
	routed := identity.RoutedAuthorizer{Kubernetes: rv, Subscribers: identity.NewSubscriberAuthorizer(cfgs)}
	auth := identity.NewAuthenticator(identity.Hasher{Key: []byte(hashKey)}, false, identity.TokenReviewVerifier{Reviewer: rv}, v)
	store := grants.NewMemory(maxGrants, nil)
	h := NewHandler(nc, "ns").WithAccess(auth, routed)
	if passToTools {
		h.WithGrants(store, []string{verifierName})
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return grantRig{srv: srv, store: store, js: js, owned: owned}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (g grantRig) ask(t *testing.T, token, conv string) *http.Response {
	t.Helper()
	return do(t, http.MethodPost, g.srv.URL+postPath, token, `{"message":"hi","conversationId":"`+conv+`"}`)
}

func (g grantRig) renew(t *testing.T, token string) *http.Response {
	t.Helper()
	return do(t, http.MethodPut, g.srv.URL+grantPath, token, "")
}

func (g grantRig) held(t *testing.T) grants.Grant {
	t.Helper()
	got, err := g.store.Get(grantConv)
	if err != nil {
		t.Fatalf("no grant for %s: %v", grantConv, err)
	}
	return got
}

func (g grantRig) noGrant(t *testing.T) {
	t.Helper()
	if _, err := g.store.Get(grantConv); err == nil {
		t.Fatalf("a grant is still held for %s", grantConv)
	}
}

func TestAVerifiedSubscriberOnAPassingVerifierHasTheirTokenHeldForTheTurn(t *testing.T) {
	r := newGrantRig(t, true, 0)
	token := personTokenUntil("person-1", time.Now().Add(time.Hour))
	if resp := r.ask(t, token, grantConv); resp.StatusCode != http.StatusOK {
		t.Fatalf("ask: %d", resp.StatusCode)
	}
	g := r.held(t)
	want := identity.Hasher{Key: []byte(hashKey)}.ID(verifierName, "person-1")
	if g.Token() != token || g.Owner.ID != want || g.Verifier != "oreilly" {
		t.Fatalf("grant %v owner %+v", g, g.Owner)
	}
	if left := time.Until(g.Expires); left < grants.MaxLifetime-time.Minute || left > grants.MaxLifetime {
		t.Fatalf("the grant ends in %s, want the 30 minute ceiling", left)
	}
}

func TestTheGrantEndsWhenTheTokenDoes(t *testing.T) {
	r := newGrantRig(t, true, 0)
	exp := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	r.ask(t, personTokenUntil("person-1", exp), grantConv)
	if got := r.held(t).Expires; !got.Equal(exp) {
		t.Fatalf("expires %s, want the token's %s", got, exp)
	}
}

func TestNoGrantWhenTheVerifierDoesNotPassTokensToTools(t *testing.T) {
	r := newGrantRig(t, false, 0)
	if resp := r.ask(t, personTokenUntil("person-1", time.Now().Add(time.Hour)), grantConv); resp.StatusCode != http.StatusOK {
		t.Fatalf("ask: %d", resp.StatusCode)
	}
	r.noGrant(t)
	if resp := r.renew(t, personTokenUntil("person-1", time.Now().Add(time.Hour))); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("renewal where tokens are not kept: %d, want 403", resp.StatusCode)
	}
}

func TestAKubernetesCallerNeverCreatesAGrant(t *testing.T) {
	r := newGrantRig(t, true, 0)
	if resp := r.ask(t, tokenAlice, "k1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("alice asks: %d", resp.StatusCode)
	}
	if r.store.Len() != 0 {
		t.Fatal("a Kubernetes token was kept")
	}
	if resp := do(t, http.MethodPut, r.srv.URL+postPath+"/k1/credential", tokenAlice, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a Kubernetes caller renewing: %d, want 403", resp.StatusCode)
	}
	if r.store.Len() != 0 {
		t.Fatal("a renewal kept a Kubernetes token")
	}
}

func TestAFullStoreRefusesTheQuestionAndQueuesNothing(t *testing.T) {
	r := newGrantRig(t, true, 1)
	exp := time.Now().Add(time.Hour)
	if resp := r.ask(t, personTokenUntil("person-1", exp), "c1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first: %d", resp.StatusCode)
	}
	if resp := r.ask(t, personTokenUntil("person-2", exp), "c2"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("beyond the bound: %d, want 503", resp.StatusCode)
	}
	stream, err := r.js.Stream(t.Context(), "KUBEMOOT_REQUEST")
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := stream.Info(t.Context()); info.State.Msgs != 1 {
		t.Fatalf("%d questions queued, want 1: the refused one must not be", info.State.Msgs)
	}
}

func TestSomeoneElsesAskOnTheSameIdLeavesTheGrantAlone(t *testing.T) {
	r := newGrantRig(t, true, 0)
	first := personTokenUntil("person-1", time.Now().Add(time.Hour))
	r.ask(t, first, grantConv)
	if resp := r.ask(t, personTokenUntil("person-2", time.Now().Add(time.Hour)), grantConv); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another person continuing it: %d, want 404", resp.StatusCode)
	}
	if r.held(t).Token() != first {
		t.Fatal("another person replaced the grant")
	}
}

func TestTheOwnerRenewsAndReplacesTheToken(t *testing.T) {
	r := newGrantRig(t, true, 0)
	r.ask(t, personTokenUntil("person-1", time.Now().Add(10*time.Minute)), grantConv)
	fresh := personTokenUntil("person-1", time.Now().Add(20*time.Minute))
	resp := r.renew(t, fresh)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("renewal: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got credentialRenewal
	if err := json.Unmarshal(body, &got); err != nil || got.ConversationID != grantConv || got.ExpiresAt == "" {
		t.Fatalf("answer %s %v", body, err)
	}
	if strings.Contains(string(body), subscriberCanary) {
		t.Fatalf("the renewal answer carries the token: %s", body)
	}
	if r.held(t).Token() != fresh {
		t.Fatal("the grant kept the old token")
	}
}

func TestATokenThatEndsEarlierDoesNotReplaceALaterOne(t *testing.T) {
	r := newGrantRig(t, true, 0)
	later := personTokenUntil("person-1", time.Now().Add(20*time.Minute))
	r.ask(t, later, grantConv)
	if resp := r.renew(t, personTokenUntil("person-1", time.Now().Add(5*time.Minute))); resp.StatusCode != http.StatusOK {
		t.Fatalf("renewal: %d", resp.StatusCode)
	}
	if r.held(t).Token() != later {
		t.Fatal("an earlier token replaced a later one")
	}
}

func TestOnlyTheOwnerMayRenew(t *testing.T) {
	r := newGrantRig(t, true, 0)
	mine := personTokenUntil("person-1", time.Now().Add(10*time.Minute))
	r.ask(t, mine, grantConv)
	if resp := r.renew(t, personTokenUntil("person-2", time.Now().Add(20*time.Minute))); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another person: %d, want 404, as for any conversation that is not theirs", resp.StatusCode)
	}
	if r.held(t).Token() != mine {
		t.Fatal("a non-owner changed the grant")
	}
	if resp := r.renew(t, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential: %d, want 401", resp.StatusCode)
	}
	if resp := do(t, http.MethodPut, r.srv.URL+postPath+"/nobody/credential", personTokenUntil("person-1", time.Now().Add(time.Hour)), ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a conversation nobody owns: %d, want 404", resp.StatusCode)
	}
}

func TestARenewalRestoresAGrantTheGatewayLost(t *testing.T) {
	r := newGrantRig(t, true, 0)
	r.ask(t, personTokenUntil("person-1", time.Now().Add(10*time.Minute)), grantConv)
	r.store.Delete(grantConv)
	fresh := personTokenUntil("person-1", time.Now().Add(20*time.Minute))
	if resp := r.renew(t, fresh); resp.StatusCode != http.StatusOK {
		t.Fatalf("renewal: %d", resp.StatusCode)
	}
	if r.held(t).Token() != fresh {
		t.Fatal("the grant was not restored")
	}
}

func TestTheGrantEndsWithTheTurnStopAndDelete(t *testing.T) {
	person := personTokenUntil("person-1", time.Now().Add(time.Hour))
	other := personTokenUntil("person-2", time.Now().Add(time.Hour))
	t.Run("synthesis and done", func(t *testing.T) {
		r := newGrantRig(t, true, 0)
		r.ask(t, person, grantConv)
		r.held(t)
		r.owned.finishThread(t, "T1", grantConv)
		resp := do(t, http.MethodGet, r.srv.URL+postPath+"/"+grantConv+"/stream", person, "")
		_, _ = io.Copy(io.Discard, resp.Body)
		r.noGrant(t)
	})
	t.Run("stop", func(t *testing.T) {
		r := newGrantRig(t, true, 0)
		r.ask(t, person, grantConv)
		do(t, http.MethodPost, r.srv.URL+postPath+"/"+grantConv+"/stop", other, "")
		r.held(t)
		do(t, http.MethodPost, r.srv.URL+postPath+"/"+grantConv+"/stop", person, "")
		r.noGrant(t)
	})
	t.Run("delete", func(t *testing.T) {
		r := newGrantRig(t, true, 0)
		r.ask(t, person, grantConv)
		do(t, http.MethodDelete, r.srv.URL+postPath+"/"+grantConv, other, "")
		r.held(t)
		do(t, http.MethodDelete, r.srv.URL+postPath+"/"+grantConv, person, "")
		r.noGrant(t)
	})
}

func TestTheTokenIsNowhereButTheLeaseAndTheGrant(t *testing.T) {
	logs := captureLogs(t)
	r := newGrantRig(t, true, 0)
	person := personTokenUntil("person-1", time.Now().Add(time.Hour))
	var bodies []string
	read := func(resp *http.Response) {
		b, _ := io.ReadAll(resp.Body)
		bodies = append(bodies, string(b))
	}
	read(r.ask(t, person, grantConv))
	read(r.renew(t, personTokenUntil("person-1", time.Now().Add(2*time.Hour))))
	read(r.renew(t, personTokenUntil("person-2", time.Now().Add(2*time.Hour))))
	read(r.ask(t, personTokenUntil("person-2", time.Now().Add(time.Hour)), grantConv))
	read(do(t, http.MethodPost, r.srv.URL+postPath+"/"+grantConv+"/stop", person, ""))
	for _, b := range bodies {
		if strings.Contains(b, subscriberCanary) {
			t.Fatalf("a response carries the token: %s", b)
		}
	}
	if strings.Contains(logs(), subscriberCanary) {
		t.Fatalf("the token is in the logs: %s", logs())
	}
	assertNoTokenOrSubjectStored(t, r.js, r.owned.kv)
}

func TestAnExpiredGrantOfAnotherPrincipalIsNotReplaced(t *testing.T) {
	r := newGrantRig(t, true, 0)
	other := identity.Hasher{Key: []byte(hashKey)}.ID(verifierName, "someone-else")
	owner := bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: verifierName, ID: other}
	_ = r.store.Put(grantConv, grants.New(owner, verifierName, "theirs", time.Now().Add(-time.Minute)))
	h := NewHandler(nil, "ns").WithGrants(r.store, []string{verifierName})
	caller := identity.Caller{
		Principal: bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: verifierName, ID: "mine"},
		Identity: &identity.Identity{Issuer: verifierName,
			Credential: identity.NewCredential(personTokenUntil("person-1", time.Now().Add(time.Hour)))},
	}
	if _, err := h.holdCredential(caller, grantConv, false); !errors.Is(err, errGrantConflict) {
		t.Fatalf("got %v, want a conflict", err)
	}
}

func TestOnlyTheOwnerReadingTheStreamEndsTheGrantAndPauseAndResumeDoNot(t *testing.T) {
	r := newGrantRig(t, true, 0)
	person := personTokenUntil("person-1", time.Now().Add(time.Hour))
	r.ask(t, person, grantConv)
	for _, ctl := range []string{"pause", "resume"} {
		do(t, http.MethodPost, r.srv.URL+postPath+"/"+grantConv+"/"+ctl, person, "")
		r.held(t)
	}
	r.owned.finishThread(t, "T1", grantConv)
	resp := do(t, http.MethodGet, r.srv.URL+postPath+"/"+grantConv+"/stream", tokenAdmin, "", reasonHeader, "audit")
	_, _ = io.Copy(io.Discard, resp.Body)
	r.held(t)
	resp = do(t, http.MethodGet, r.srv.URL+postPath+"/"+grantConv+"/stream", person, "")
	_, _ = io.Copy(io.Discard, resp.Body)
	r.noGrant(t)
}
