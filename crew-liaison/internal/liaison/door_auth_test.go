package liaison

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	doorJane    = "door-jane"
	doorJoe     = "door-joe"
	doorBad     = "door-bad"
	janeUser    = "oidc:jane"
	hopAudience = "kubemoot-crew/hello/hello"
)

// stubDoor stands in for the door's verifier: the signin package tests the real one.
type stubDoor struct{}

func (stubDoor) Verify(r *http.Request) (*identity.Identity, error) {
	switch identity.BearerToken(r) {
	case doorJane:
		return &identity.Identity{Issuer: identity.IssuerKubernetes, Subject: janeUser, Groups: []string{"oidc:team-a"}}, nil
	case doorJoe:
		return &identity.Identity{Issuer: identity.IssuerKubernetes, Subject: "oidc:joe"}, nil
	case doorBad:
		return nil, identity.ErrInvalid
	}
	return nil, identity.ErrNoCredential
}

func hopKeyPair(t *testing.T) (*identity.HopSigner, *identity.HopVerifier) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	signer, err := identity.NewHopSigner(key, "liaison", nil)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := identity.NewHopVerifier([]identity.TrustedHopConfig{{
		Name: "liaison", Audience: hopAudience, PublicKeys: []string{string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return signer, verifier
}

func doorFront(signer *identity.HopSigner) *SignInFront {
	front := frontStub()
	front.Verifier, front.Hop = stubDoor{}, signer
	return front
}

// TestMCPAcceptsDoorTokensAndRefusesTheRest checks every kind of credential against a door
// with and without the shared token and Kubernetes tokens behind it.
func TestMCPAcceptsDoorTokensAndRefusesTheRest(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	cases := []struct {
		name  string
		token string
		auth  *identity.Authenticator
		with  map[string]int // the credential presented -> status
	}{
		{"door only", "", nil, map[string]int{
			doorJane: http.StatusOK, doorBad: http.StatusUnauthorized, "": http.StatusUnauthorized,
			"a-service-token": http.StatusUnauthorized, sharedToken: http.StatusUnauthorized,
		}},
		{"with shared token", sharedToken, nil, map[string]int{
			doorJane: http.StatusOK, doorBad: http.StatusUnauthorized, sharedToken: http.StatusOK, "": http.StatusUnauthorized,
		}},
		{"with kubernetes tokens", "", clusterAuth(false), map[string]int{
			doorJane: http.StatusOK, doorBad: http.StatusUnauthorized, tokenAlice: http.StatusOK, "nobody": http.StatusUnauthorized,
		}},
	}
	for _, c := range cases {
		srv := httptest.NewServer(NewHandlerWithFront(newTestService(t, f, 1), c.token, c.auth, doorFront(nil)))
		for presented, want := range c.with {
			authorization := ""
			if presented != "" {
				authorization = "Bearer " + presented
			}
			// GET on /mcp is answered by the MCP handler once the door lets the request through.
			got := status(t, srv.URL+"/mcp", authorization)
			if (want == http.StatusOK) != (got != http.StatusUnauthorized) {
				t.Errorf("%s: presenting %q gave %d, want %d", c.name, presented, got, want)
			}
		}
		srv.Close()
	}
}

func TestAChallengeFromTheDoorNamesTheMetadata(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandlerWithFront(newTestService(t, f, 1), "", nil, doorFront(nil)))
	defer srv.Close()
	want := `Bearer realm="kubemoot-crew-liaison", resource_metadata="` + metadataURL + `"`
	if got := challengeOf(t, srv.URL); got != want {
		t.Errorf("challenge %q", got)
	}
}

// TestADoorCallersQuestionCarriesAnAssertionNotTheirToken is the hop: the crew's gateway gets
// a signed assertion for that crew alone, and a ticket belongs to the person who asked.
func TestADoorCallersQuestionCarriesAnAssertionNotTheirToken(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	signer, verifier := hopKeyPair(t)
	svc := newTestService(t, f, 4)
	srv := httptest.NewServer(NewHandlerWithFront(svc, "", nil, doorFront(signer)))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	jane := connectSession(ctx, t, srv.URL+"/mcp", doorJane)
	var asked AskOutput
	out := callTool(ctx, t, jane, "ask", map[string]any{argCrew: testCrew, argQuestion: testQuestion, argWaitSeconds: 0})
	if err := json.Unmarshal([]byte(out), &asked); err != nil || asked.ID == "" {
		t.Fatalf("%v %s", err, out)
	}
	settle(t, svc, asked.ID)
	checkAssertions(t, f.authorizations(), verifier)

	joe := connectSession(ctx, t, srv.URL+"/mcp", doorJoe)
	if res := toolText(ctx, t, joe, "get_answer", map[string]any{argTicket: asked.ID}); !strings.Contains(res, "unknown or expired ticket") {
		t.Fatalf("joe read jane's ticket: %s", res)
	}
}

// checkAssertions checks that every call to the gateway carried a valid assertion of jane.
func checkAssertions(t *testing.T, calls []string, verifier *identity.HopVerifier) {
	t.Helper()
	if len(calls) < 2 {
		t.Fatalf("want the question and the stream, got %v", calls)
	}
	for i, got := range calls {
		if strings.Contains(got, doorJane) {
			t.Fatalf("call %d forwarded the door token", i)
		}
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", got)
		id, err := verifier.Verify(req)
		if err != nil || id.Subject != janeUser || len(id.Groups) != 1 || id.Groups[0] != "oidc:team-a" {
			t.Errorf("call %d: assertion = %+v, %v", i, id, err)
		}
	}
}

func TestADoorWithNoHopKeyAssertsNobody(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 4)
	srv := httptest.NewServer(NewHandlerWithFront(svc, "", nil, doorFront(nil)))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	jane := connectSession(ctx, t, srv.URL+"/mcp", doorJane)
	var asked AskOutput
	_ = json.Unmarshal([]byte(callTool(ctx, t, jane, "ask", map[string]any{argCrew: testCrew, argQuestion: testQuestion, argWaitSeconds: 0})), &asked)
	settle(t, svc, asked.ID)
	for _, got := range f.authorizations() {
		if got != "" {
			t.Errorf("a credential reached the gateway with no hop key: %q", got)
		}
	}
}

// TestAPersonWhoCannotBeAssertedIsRefusedNotSentAsAnonymous: a name the gateway would refuse must not
// become a question with no credential, which a crew that accepts anonymous callers would answer.
func TestAPersonWhoCannotBeAssertedIsRefusedNotSentAsAnonymous(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	signer, _ := hopKeyPair(t)
	front := doorFront(signer)
	front.Verifier = systemDoor{}
	svc := newTestService(t, f, 4)
	srv := httptest.NewServer(NewHandlerWithFront(svc, "", nil, front))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connectSession(ctx, t, srv.URL+"/mcp", doorJane)
	res := toolText(ctx, t, session, "ask", map[string]any{argCrew: testCrew, argQuestion: testQuestion, argWaitSeconds: 0})
	if !strings.Contains(res, "cannot be asserted") {
		t.Fatalf("the question was not refused: %s", res)
	}
	if len(f.asked()) != 0 {
		t.Errorf("a question reached the gateway: %v", f.asked())
	}
}

// systemDoor signs in a person whose name a gateway would refuse.
type systemDoor struct{}

func (systemDoor) Verify(*http.Request) (*identity.Identity, error) {
	return &identity.Identity{Issuer: identity.IssuerKubernetes, Subject: "system:admin"}, nil
}
