package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/liaison"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	envAccountsOn = "LIAISON_ACCOUNTS_ENABLED"
	trueValue     = "true"
)

func memoryState(t *testing.T) *tokenstore.Opener {
	t.Helper()
	op, err := tokenstore.Config{Backend: tokenstore.BackendMemory}.Open()
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestAccountsAreOffUnlessEnabled(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	for name, env := range map[string]map[string]string{
		"nothing set": nil,
		"disabled":    {envAccountsOn: "false"},
	} {
		svc, err := buildAccounts(envMap(env), memoryState(t), reader, logr.Discard())
		if err != nil || svc != nil {
			t.Errorf("%s: %v %v", name, svc, err)
		}
	}
}

func TestAccountsOnBuildsAServiceAndRefusesBadSettings(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := os.WriteFile(keys, []byte(sealKeys()), 0o600); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	on := map[string]string{
		envAccountsOn: trueValue, "LIAISON_ACCOUNTS_SEAL_KEYS_FILE": keys,
		"LIAISON_ACCOUNTS": `{"library":{"clientId":"cid"}}`,
	}
	svc, err := buildAccounts(envMap(on), memoryState(t), reader, logr.Discard())
	if err != nil || svc == nil || svc.Regs["library"].ClientID != "cid" {
		t.Fatalf("on: %v %v", svc, err)
	}
	noKeys := map[string]string{envAccountsOn: trueValue}
	if _, err := buildAccounts(envMap(noKeys), memoryState(t), reader, logr.Discard()); err == nil {
		t.Fatal("enabled without seal keys")
	}
	bad := map[string]string{envAccountsOn: trueValue, "LIAISON_ACCOUNTS_SEAL_KEYS_FILE": keys}
	_ = os.WriteFile(keys, []byte("not a key"), 0o600)
	if _, err := buildAccounts(envMap(bad), memoryState(t), reader, logr.Discard()); err == nil {
		t.Fatal("a malformed seal key")
	}
}

func TestAccountsNeedTheDoor(t *testing.T) {
	if _, err := signInFront(logr.Discard(), memoryState(t), &accounts.Service{}); err == nil {
		t.Fatal("connected accounts without the door's identity must be refused")
	}
}

func TestBrokerServerRefusesACallerWithoutAToken(t *testing.T) {
	svc := &accounts.Service{Broker: accounts.NewBroker(nil, nil, nil, nil, nil)}
	srv := brokerServer(svc, identity.Reviewer{Audiences: []string{liaison.AccountBrokerAudience}}, "8081")
	if srv.Addr != ":8081" || srv.ReadHeaderTimeout == 0 || srv.WriteTimeout == 0 {
		t.Fatalf("the server: %+v", srv)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/lease", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a lease with no ServiceAccount token: %d", rec.Code)
	}
	if rec2 := httptest.NewRecorder(); func() bool {
		srv.Handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/health", nil))
		return rec2.Code == http.StatusNotFound
	}() == false {
		t.Fatal("the internal port serves only the broker routes")
	}
}
