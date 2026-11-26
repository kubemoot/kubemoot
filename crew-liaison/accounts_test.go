package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
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
