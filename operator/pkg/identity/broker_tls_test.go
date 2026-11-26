package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/catransport/catest"
)

// tlsBroker is an https server standing for the account broker, answering that the person is connected.
func tlsBroker(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connected":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func brokerEnv(t *testing.T, url, caFile string) func(string) string {
	t.Helper()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(brokerSAToken), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{EnvAccountBrokerURL: url, EnvAccountBrokerTokenFile: token, EnvAccountBrokerCAFile: caFile}
	return func(k string) string { return env[k] }
}

func TestAnHTTPSBrokerIsVerifiedAgainstTheCAFile(t *testing.T) {
	srv := tlsBroker(t)
	trusting, err := connectionsFromEnv(brokerEnv(t, srv.URL, catest.WriteCA(t, srv)))
	if err != nil || trusting == nil {
		t.Fatalf("the checker: %v %v", trusting, err)
	}
	if ok, err := trusting.Connected(context.Background(), "p", libraryName); !ok || err != nil {
		t.Fatalf("a broker signed by the install CA: %v %v", ok, err)
	}
	unknown, err := connectionsFromEnv(brokerEnv(t, srv.URL, catest.UnrelatedCA(t)))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := unknown.Connected(context.Background(), "p", libraryName); ok || err == nil {
		t.Fatalf("a broker signed by an unknown CA was trusted: %v %v", ok, err)
	}
}

func TestAnHTTPSBrokerWithNoCAFileFailsClosed(t *testing.T) {
	srv := tlsBroker(t)
	checker, err := connectionsFromEnv(brokerEnv(t, srv.URL, ""))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := checker.Connected(context.Background(), "p", libraryName); ok || err == nil {
		t.Fatalf("an https broker was trusted without the install CA: %v %v", ok, err)
	}
}

func TestABrokerCAFileThatCannotBeReadRefusesTheSetting(t *testing.T) {
	srv := tlsBroker(t)
	missing := filepath.Join(t.TempDir(), "none")
	if _, err := connectionsFromEnv(brokerEnv(t, srv.URL, missing)); err == nil {
		t.Fatal("a missing CA file was accepted by the connection checker")
	}
	if _, err := AccountLeaserFromEnv(brokerEnv(t, srv.URL, missing)); err == nil {
		t.Fatal("a missing CA file was accepted by the account leaser")
	}
}

func TestAnHTTPBrokerIgnoresTheCAFile(t *testing.T) {
	checker, err := connectionsFromEnv(brokerEnv(t, liaisonBroker, filepath.Join(t.TempDir(), "none")))
	if err != nil || checker == nil {
		t.Fatalf("an http broker: %v %v", checker, err)
	}
}
