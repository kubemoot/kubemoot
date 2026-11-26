package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
)

const (
	hopIssuer = "liaison"
	hopJane   = "oidc:jane"
	hopJoe    = "oidc:joe"
	hopTeam   = "oidc:team-a"
	hopAud    = "kubemoot-crew/ns/pilot"
)

// hopCluster is an API server that knows no tokens: it answers only the access question, for the
// users and the group the test names.
func hopCluster(users []string, group string) *fake.Clientset {
	c := fake.NewSimpleClientset()
	// The API server knows no assertion as a token: a review of one is "not authenticated".
	c.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{}, nil
	})
	c.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		ra := sar.Spec.ResourceAttributes
		ok := ra.Verb == "get" && ra.Resource == "crews" && ra.Subresource == "" &&
			(slices.Contains(users, sar.Spec.User) || slices.Contains(sar.Spec.Groups, group))
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: ok}}, nil
	})
	return c
}

type hopSetup struct {
	srv    *httptest.Server
	key    *ecdsa.PrivateKey
	signer func(clock func() time.Time, key *ecdsa.PrivateKey) *identity.HopSigner
}

// newHopGateway is a gateway built as the operator builds it for a crew that lists a trusted
// hop: from the environment, so the same code runs as in a cluster.
func newHopGateway(t *testing.T, nc *natsclient.Client, client kubernetes.Interface, tokenReview bool) hopSetup {
	t.Helper()
	return newAskersGateway(t, nc, client, tokenReview, `{"kubernetesRBAC":{}}`)
}

// newAskersGateway is newHopGateway with the crew's askers rule (the JSON the operator writes).
func newAskersGateway(t *testing.T, nc *natsclient.Client, client kubernetes.Interface, tokenReview bool, askers string) hopSetup {
	t.Helper()
	return newAskersGatewayEnv(t, nc, client, tokenReview, askers, nil)
}

// newAskersGatewayEnv is newAskersGateway with more environment, such as the account broker's.
func newAskersGatewayEnv(
	t *testing.T, nc *natsclient.Client, client kubernetes.Interface, tokenReview bool, askers string, extra map[string]string,
) hopSetup {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	hops, _ := json.Marshal([]identity.TrustedHopConfig{{
		Name: hopIssuer, Audience: hopAud, PublicKeys: []string{string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))},
	}})
	env := map[string]string{
		identity.EnvEnforce: identity.EnvTrue, identity.EnvTrustedHops: string(hops), identity.EnvAskers: strings.TrimSuffix(askers, "}") + `,"namespace":"ns","crew":"pilot"}`,
		identity.EnvHashKey: "install-key",
	}
	if tokenReview {
		env[identity.EnvTokenReview] = identity.EnvTrue
	}
	for k, v := range extra {
		env[k] = v
	}
	settings, err := identity.FromEnv(func(k string) string { return env[k] },
		func() (kubernetes.Interface, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(nc, "ns").WithAccess(settings.Authenticator, settings.Authorizer).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return hopSetup{srv: srv, key: key, signer: func(clock func() time.Time, k *ecdsa.PrivateKey) *identity.HopSigner {
		s, err := identity.NewHopSigner(k, hopIssuer, clock)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}}
}

func (h hopSetup) assert(t *testing.T, user string, groups ...string) string {
	t.Helper()
	token, err := h.signer(nil, h.key).Assert(hopAud, user, groups)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAnAssertedPersonWhoMayUseTheCrewIsTheCallerAndABodyPrincipalIsIgnored(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	h := newHopGateway(t, nc, hopCluster([]string{hopJane}, ""), false)
	body := `{"message":"hi","principal":{"kind":"kubernetes","id":"someone-else"}}`
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJane), body); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	p := queued(t, js).Principal
	want := identity.Hasher{}.ID(identity.IssuerKubernetes, hopJane)
	if p == nil || p.Kind != bus.PrincipalKindKubernetes || p.ID != want {
		t.Fatalf("principal %+v, want id %s", p, want)
	}
}

func TestAnAssertedPersonRBACRefusesIsRefused(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	h := newHopGateway(t, nc, hopCluster([]string{hopJane}, ""), false)
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJoe), `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403 from the SubjectAccessReview", resp.StatusCode)
	}
}

func TestTheAccessCheckNamesTheAssertedGroups(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	h := newHopGateway(t, nc, hopCluster(nil, hopTeam), false)
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJoe, hopTeam), `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("a member of the allowed group: %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJoe, "oidc:other"), `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a member of another group: %d", resp.StatusCode)
	}
}

func TestAssertionsThatAreNotValidAreRefusedBeforeAnyAccessCheck(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	client := hopCluster([]string{hopJane}, "")
	h := newHopGateway(t, nc, client, true)
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sign := func(clock func() time.Time, key *ecdsa.PrivateKey, aud string) string {
		tok, err := h.signer(clock, key).Assert(aud, hopJane, nil)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	ago := func(d time.Duration) func() time.Time { return func() time.Time { return time.Now().Add(-d) } }
	for name, token := range map[string]string{
		"expired":                   sign(ago(5*time.Minute), h.key, hopAud),
		"replayed past its minute":  sign(ago(identity.HopLifetime+time.Second), h.key, hopAud),
		"another crew's audience":   sign(nil, h.key, "kubemoot-crew/ns/other"),
		"signed by another key":     sign(nil, otherKey, hopAud),
		"a token that is not a hop": "eyJhbGciOiJSUzI1NiJ9.e30.c2ln",
	} {
		before := len(client.Actions())
		if resp := do(t, http.MethodPost, h.srv.URL+postPath, token, `{"message":"hi"}`); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
		for _, a := range client.Actions()[before:] {
			if a.GetResource().Resource == "subjectaccessreviews" {
				t.Errorf("%s: an access check was made for a caller that is not verified", name)
			}
		}
	}
}

func TestAHopAssertionMeansNothingToACrewThatListsNoHop(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	srv := enforcingServer(t, nc, false)
	h := newHopGateway(t, nc, hopCluster([]string{hopJane}, ""), false)
	if resp := do(t, http.MethodPost, srv.URL+postPath, h.assert(t, hopJane), `{"message":"hi"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: the API server does not know the assertion", resp.StatusCode)
	}
}

func TestOneAssertedPersonCannotReadAnothersConversation(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	h := newHopGateway(t, nc, hopCluster([]string{hopJane, hopJoe}, ""), false)
	start := `{"message":"hi","conversationId":"c1"}`
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJane), start); resp.StatusCode != http.StatusOK {
		t.Fatalf("jane starts: %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJoe), start); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("joe continuing jane's conversation: %d, want 404", resp.StatusCode)
	}
	stream := h.srv.URL + listPath + "/c1/stream"
	if resp := do(t, http.MethodGet, stream, h.assert(t, hopJoe), "", reasonHeader, "curious"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("joe reading jane's conversation with a reason: %d, want 404: that is a Kubernetes right", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJane), start); resp.StatusCode != http.StatusOK {
		t.Fatalf("jane continues: %d", resp.StatusCode)
	}
}
