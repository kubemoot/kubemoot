package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
)

const (
	brokerSAToken = "gateway-projected-token"
	libraryName   = "library"
	liaisonBroker = "http://liaison:8081"
)

// stubConnections answers by person.
type stubConnections struct {
	mu     sync.Mutex
	live   map[string]bool
	err    error
	asked  []string
	forAcc string
}

func (s *stubConnections) Connected(_ context.Context, principalID, account string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked, s.forAcc = append(s.asked, principalID), account
	return s.live[principalID], s.err
}

func connectedRule() AskersConfig {
	return AskersConfig{ConnectedAccount: libraryName, Namespace: teamNS, Crew: crewName}
}

func askUse(t *testing.T, a AskerAuthorizer, id *Identity) (bool, error) {
	t.Helper()
	return a.Allowed(context.Background(), id, UseAccess(teamNS, crewName))
}

func TestConnectedAccountAdmitsOnlyAPersonWhoHoldsTheConnection(t *testing.T) {
	hasher := Hasher{Key: []byte("install-key")}
	person := asker()
	conns := &stubConnections{live: map[string]bool{hasher.ID(person.Issuer, person.Subject): true}}
	a := AskerAuthorizer{Rule: connectedRule(), Connections: conns, Hasher: hasher, Log: logr.Discard()}
	if ok, err := askUse(t, a, person); !ok || err != nil {
		t.Fatalf("a connected person: %v %v", ok, err)
	}
	stranger := &Identity{Issuer: person.Issuer, Subject: "oidc:someone-else"}
	if ok, err := askUse(t, a, stranger); ok || err != nil {
		t.Fatalf("a person with no connection: %v %v", ok, err)
	}
	if conns.forAcc != libraryName {
		t.Errorf("the account asked about: %q", conns.forAcc)
	}
	for _, id := range conns.asked {
		if strings.Contains(id, person.Subject) {
			t.Error("the broker must be asked by id, never by user name")
		}
	}
}

func TestConnectedAccountRefusesWhenTheConnectionCannotBeChecked(t *testing.T) {
	hasher := Hasher{Key: []byte("install-key")}
	cases := map[string]AskerAuthorizer{
		"no checker":       {Rule: connectedRule(), Hasher: hasher, Log: logr.Discard()},
		"no install key":   {Rule: connectedRule(), Connections: &stubConnections{live: map[string]bool{"": true}}, Log: logr.Discard()},
		"broker not there": {Rule: connectedRule(), Hasher: hasher, Connections: &stubConnections{err: ErrConnectionCheck}, Log: logr.Discard()},
	}
	for name, a := range cases {
		if ok, _ := askUse(t, a, asker()); ok {
			t.Errorf("%s: admitted", name)
		}
	}
	a := cases["broker not there"]
	if _, err := askUse(t, a, asker()); !errors.Is(err, ErrConnectionCheck) {
		t.Errorf("an unanswered check must be an error, not a no: %v", err)
	}
}

func brokerServer(t *testing.T, handler http.HandlerFunc) (*BrokerConnections, *string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(brokerSAToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewBrokerConnections(srv.URL, file, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b, &file
}

func TestBrokerConnectionsAskWithTheGatewaysOwnToken(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]string
	b, _ := brokerServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"connected":true}`))
	})
	ok, err := b.Connected(context.Background(), "abc123", libraryName)
	if err != nil || !ok || gotAuth != "Bearer "+brokerSAToken || gotPath != connectedPath ||
		gotBody["principalId"] != "abc123" || gotBody["account"] != libraryName {
		t.Fatalf("%v %v auth=%q path=%q body=%v", ok, err, gotAuth, gotPath, gotBody)
	}
}

func TestBrokerConnectionsNeverTurnAnUnclearAnswerIntoAYes(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"not connected":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"connected":false}`)) },
		"forbidden":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"server error":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"garbage":         func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`yes`)) },
		"no field":        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) },
		"redirect to yes": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/yes", http.StatusFound) },
	} {
		b, _ := brokerServer(t, handler)
		if ok, _ := b.Connected(context.Background(), "p", libraryName); ok {
			t.Errorf("%s: admitted", name)
		}
	}
}

func TestBrokerConnectionsErrorsNeverCarryTheToken(t *testing.T) {
	b, file := brokerServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, err := b.Connected(context.Background(), "p", libraryName)
	if err == nil || strings.Contains(err.Error(), brokerSAToken) || !errors.Is(err, ErrConnectionCheck) {
		t.Fatalf("a failing broker: %v", err)
	}
	if err := os.WriteFile(*file, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Connected(context.Background(), "p", libraryName); !errors.Is(err, ErrConnectionCheck) {
		t.Fatalf("an empty token file: %v", err)
	}
	if err := os.Remove(*file); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Connected(context.Background(), "p", libraryName); !errors.Is(err, ErrConnectionCheck) {
		t.Fatalf("a missing token file: %v", err)
	}
}

func TestBrokerConnectionsAreCheckedAtConstruction(t *testing.T) {
	for name, c := range map[string][2]string{
		"not a url": {"::", "/t"}, "no host": {"https://", "/t"}, "ftp": {"ftp://x", "/t"}, "no token file": {"https://x", ""},
	} {
		if _, err := NewBrokerConnections(c[0], c[1], nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestBrokerUnreachableIsACheckFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	file := filepath.Join(t.TempDir(), "t")
	_ = os.WriteFile(file, []byte("tok"), 0o600)
	b, _ := NewBrokerConnections(url, file, nil)
	if _, err := b.Connected(context.Background(), "p", libraryName); !errors.Is(err, ErrConnectionCheck) {
		t.Fatalf("%v", err)
	}
}

func TestConnectionsFromEnv(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if c, err := connectionsFromEnv(get(nil)); c != nil || err != nil {
		t.Fatalf("no broker: %v %v", c, err)
	}
	if _, err := connectionsFromEnv(get(map[string]string{EnvAccountBrokerURL: liaisonBroker})); err == nil {
		t.Fatal("a broker address with no token file")
	}
	ok := map[string]string{EnvAccountBrokerURL: liaisonBroker, EnvAccountBrokerTokenFile: "/t"}
	if c, err := connectionsFromEnv(get(ok)); c == nil || err != nil {
		t.Fatalf("a configured broker: %v %v", c, err)
	}
}

func TestFromEnvGivesAConnectedAccountRuleItsBroker(t *testing.T) {
	env := hopEnv(t, `{"connectedAccount":"library"}`, map[string]string{
		EnvHashKey: "install-key", EnvAccountBrokerURL: liaisonBroker, EnvAccountBrokerTokenFile: "/t",
	})
	s, err := FromEnv(envFor(env), func() (kubernetes.Interface, error) { return nil, errors.New("no") })
	if err != nil || s.Authorizer == nil {
		t.Fatal(err)
	}
	bad := hopEnv(t, `{"connectedAccount":"library"}`, map[string]string{EnvHashKey: "k", EnvAccountBrokerURL: liaisonBroker})
	if _, err := FromEnv(envFor(bad), nil); err == nil {
		t.Fatal("a broker address with no token file must fail at start")
	}
}
