package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"github.com/nats-io/nats.go/jetstream"

	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
)

const (
	subscriberCanary = "CANARY-TOKEN-1"
	hashKey          = "install-hash-key"
)

// personService is a fake of the service that accepts people's tokens.
type personService struct {
	srv    *httptest.Server
	status int
}

func newPersonService(t *testing.T, status int) *personService {
	t.Helper()
	p := &personService{status: status}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(p.status) }))
	t.Cleanup(p.srv.Close)
	return p
}

// personToken is a token for subject whose signature is the canary.
func personToken(subject string) string {
	payload, _ := json.Marshal(map[string]any{
		"upstream_claims": map[string]any{"sub": subject}, "exp": time.Now().Add(time.Hour).Unix()})
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString(payload) + "." + subscriberCanary
}

// subscriberServer is a gateway that verifies Kubernetes tokens against cluster() and people's
// tokens against svc, with the authorizer FromEnv builds. withPolicy false leaves the
// subscriber authorizer out, as a crew with no verifier configured for it would have.
func subscriberServer(t *testing.T, nc *natsclient.Client, svc *personService, authorize string, withPolicy bool) *httptest.Server {
	t.Helper()
	raw := `[{"name":"oreilly","verifyURL":"` + svc.srv.URL + `","subjectClaim":"upstream_claims.sub"` + authorize + `}]`
	cfgs, err := identity.ParseServiceTokens(raw)
	if err != nil {
		t.Fatal(err)
	}
	v, err := identity.NewServiceTokenVerifier(cfgs[0], []byte(hashKey), svc.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	rv := identity.Reviewer{Client: cluster()}
	routed := identity.RoutedAuthorizer{Kubernetes: rv}
	if withPolicy {
		routed.Subscribers = identity.NewSubscriberAuthorizer(cfgs)
	}
	auth := identity.NewAuthenticator(identity.Hasher{Key: []byte(hashKey)}, false, identity.TokenReviewVerifier{Reviewer: rv}, v)
	mux := http.NewServeMux()
	NewHandler(nc, "ns").WithAccess(auth, routed).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGuardAdmitsASubscriberOnlyWithTheVerifierConfiguredForIt(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	svc := newPersonService(t, 200)

	open := subscriberServer(t, nc, svc, "", true)
	if resp := do(t, http.MethodPost, open.URL+postPath, personToken("acct-42"), `{"message":"hi","conversationId":"c1"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("a configured verifier: status %d, want 200", resp.StatusCode)
	}
	p := queued(t, js).Principal
	want := identity.Hasher{Key: []byte(hashKey)}.ID("oreilly", "acct-42")
	if p == nil || p.Kind != bus.PrincipalKindSubscriber || p.Issuer != "oreilly" || p.ID != want {
		t.Fatalf("principal %+v, want id %s", p, want)
	}

	noPolicy := subscriberServer(t, nc, svc, "", false)
	if resp := do(t, http.MethodPost, noPolicy.URL+postPath, personToken("acct-42"), `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no policy for subscribers: status %d, want 403", resp.StatusCode)
	}

	listed := subscriberServer(t, nc, svc, `,"authorize":"Subjects","subjects":["someone-else"]`, true)
	if resp := do(t, http.MethodPost, listed.URL+postPath, personToken("acct-42"), `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a subject not on the list: status %d, want 403", resp.StatusCode)
	}
}

func TestGuardRefusesAPersonTheServiceRefusesAndFailsClosedWhenItCannotAsk(t *testing.T) {
	nc, _ := jetStreamServer(t)
	svc := newPersonService(t, 401)
	srv := subscriberServer(t, nc, svc, "", true)
	if resp := do(t, http.MethodPost, srv.URL+postPath, personToken("a"), `{"message":"hi"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refused token: status %d, want 401", resp.StatusCode)
	}
	svc.status = 500
	if resp := do(t, http.MethodPost, srv.URL+postPath, personToken("b"), `{"message":"hi"}`); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("service failure: status %d, want 503", resp.StatusCode)
	}
	svc.srv.Close()
	if resp := do(t, http.MethodPost, srv.URL+postPath, personToken("c"), `{"message":"hi"}`); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("service down: status %d, want 503", resp.StatusCode)
	}
}

func TestKubernetesCallersKeepRBACBesideSubscribers(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	srv := subscriberServer(t, nc, newPersonService(t, 200), "", true)
	if resp := do(t, http.MethodPost, srv.URL+postPath, tokenAlice, `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("alice: %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, srv.URL+postPath, tokenNoRole, `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a caller RBAC refuses: %d", resp.StatusCode)
	}
}

func TestASubscriberCannotReadAnotherCallersConversationsAndTheTokenNeverReachesTheBus(t *testing.T) {
	nc, js := jetStreamServer(t)
	o := newOwnedSetupOn(t, nc, js, false)
	srv := subscriberServer(t, nc, newPersonService(t, 200), "", true)
	mine, other := personToken("person-1"), personToken("person-2")
	if resp := do(t, http.MethodPost, srv.URL+postPath, mine, `{"message":"hi","conversationId":"c1"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	stream := srv.URL + listPath + "/c1/stream"
	if resp := do(t, http.MethodGet, stream, other, "", reasonHeader, "curious"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another subscriber with a reason: %d, want 404: operator reads are a Kubernetes right", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, srv.URL+postPath, other, `{"message":"hi","conversationId":"c1"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another subscriber continuing it: %d, want 404", resp.StatusCode)
	}

	assertNoTokenOrSubjectStored(t, js, o.kv)
	rec, err := o.kv.Get(context.Background(), bus.ConversationOwnerKey("ns", "pilot", "c1"))
	if err != nil {
		t.Fatal(err)
	}
	var owner bus.ConversationOwner
	if err := json.Unmarshal(rec.Value(), &owner); err != nil || owner.Owner.Kind != bus.PrincipalKindSubscriber {
		t.Fatalf("owner %s %v", rec.Value(), err)
	}
}

// assertNoTokenOrSubjectStored scans the request stream and every key-value entry the gateway
// wrote: none holds the token or the person's raw subject.
func assertNoTokenOrSubjectStored(t *testing.T, js jetstream.JetStream, kv jetstream.KeyValue) {
	t.Helper()
	ctx := context.Background()
	st, err := js.Stream(ctx, "KUBEMOOT_REQUEST")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := st.Info(ctx)
	for seq := uint64(1); seq <= info.State.LastSeq; seq++ {
		if msg, err := st.GetMsg(ctx, seq); err == nil && strings.Contains(string(msg.Data), subscriberCanary) {
			t.Fatalf("the token is on the bus: %s", msg.Data)
		}
	}
	keys, _ := kv.Keys(ctx)
	for _, k := range keys {
		e, _ := kv.Get(ctx, k)
		if strings.Contains(string(e.Value()), subscriberCanary) || strings.Contains(string(e.Value()), "person-1") {
			t.Fatalf("%s holds the token or the subject: %s", k, e.Value())
		}
	}
}
