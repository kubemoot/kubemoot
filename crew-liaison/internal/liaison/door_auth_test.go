package liaison

import (
	"net/http"
	"net/http/httptest"
	"testing"

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

func doorFront() *SignInFront {
	front := frontStub()
	front.Verifier = stubDoor{}
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
		srv := httptest.NewServer(NewHandlerWithFront(newTestService(t, f, 1), c.token, c.auth, doorFront()))
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
	srv := httptest.NewServer(NewHandlerWithFront(newTestService(t, f, 1), "", nil, doorFront()))
	defer srv.Close()
	want := `Bearer realm="kubemoot-crew-liaison", resource_metadata="` + metadataURL + `"`
	if got := challengeOf(t, srv.URL); got != want {
		t.Errorf("challenge %q", got)
	}
}
