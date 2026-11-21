package liaison

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const metadataURL = "https://liaison.example.test/.well-known/oauth-protected-resource/mcp"

func frontStub() *SignInFront {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("front")) })
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("front")) })
	return &SignInFront{Handler: mux, ResourceMetadataURL: metadataURL}
}

func challengeOf(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	return resp.Header.Get("WWW-Authenticate")
}

func TestUnauthorizedNamesTheSignInMetadata(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	for name, handler := range map[string]http.Handler{
		"shared token only": NewHandlerWithFront(newTestService(t, f, 1), sharedToken, nil, frontStub()),
		"verified callers":  NewHandlerWithFront(newTestService(t, f, 1), "", clusterAuth(false), frontStub()),
	} {
		srv := httptest.NewServer(handler)
		got := challengeOf(t, srv.URL)
		srv.Close()
		want := `Bearer realm="kubemoot-crew-liaison", resource_metadata="` + metadataURL + `"`
		if got != want {
			t.Errorf("%s: challenge %q", name, got)
		}
	}
}

func TestChallengeIsUnchangedWithoutAFront(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandlerWithAuth(newTestService(t, f, 1), sharedToken, identity.AnonymousAuthenticator()))
	defer srv.Close()
	if got := challengeOf(t, srv.URL); got != `Bearer realm="kubemoot-crew-liaison"` {
		t.Errorf("challenge %q", got)
	}
	// No front: the OAuth routes do not exist.
	if got := status(t, srv.URL+"/oauth/register", ""); got != http.StatusNotFound {
		t.Errorf("oauth route without a front: %d", got)
	}
}

func TestFrontRoutesAreMountedOutsideTheDoor(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	srv := httptest.NewServer(NewHandlerWithFront(newTestService(t, f, 1), sharedToken, nil, frontStub()))
	defer srv.Close()
	// The sign-in routes must be reachable with no credential, or nobody could sign in.
	for _, path := range []string{"/oauth/register", "/.well-known/oauth-authorization-server"} {
		if got := status(t, srv.URL+path, ""); got != http.StatusOK {
			t.Errorf("%s: %d", path, got)
		}
	}
	if got := status(t, srv.URL+"/mcp", ""); got != http.StatusUnauthorized {
		t.Errorf("/mcp stays gated: %d", got)
	}
}
