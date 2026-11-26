package tokenstore_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

const (
	envBackend = "LIAISON_STATE_BACKEND"
	bao        = "openbao"
	natsName   = "nats"
	envNATSURL = "NATS_URL"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
		shared  bool
	}{
		{"default is memory", nil, "memory", "", false},
		{natsName, map[string]string{envBackend: "NATS", envNATSURL: "nats://x:4222"}, natsName, "", true},
		{"nats without a url", map[string]string{envBackend: "nats"}, "", envNATSURL, false},
		{"nats with half a client certificate", map[string]string{envBackend: "nats", envNATSURL: "nats://x", "NATS_CLIENT_CERT_FILE": "c"}, "", "together", false},
		{bao, map[string]string{envBackend: bao, "LIAISON_OPENBAO_ADDR": "https://b", "LIAISON_OPENBAO_TOKEN_FILE": "/t"}, bao, "", true},
		{"openbao without a token file", map[string]string{envBackend: bao, "LIAISON_OPENBAO_ADDR": "https://b"}, "", "LIAISON_OPENBAO_TOKEN_FILE", false},
		{"unknown", map[string]string{envBackend: "etcd"}, "", "must be memory", false},
	}
	for _, c := range cases {
		got, err := tokenstore.ConfigFromEnv(envOf(c.env))
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: error %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.Backend != c.want || got.Shared() != c.shared {
			t.Errorf("%s: %+v %v", c.name, got, err)
		}
	}
}

func TestOpenerMemoryKeepsPurposesApart(t *testing.T) {
	cfg, _ := tokenstore.ConfigFromEnv(envOf(nil))
	o, err := cfg.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	a, _ := o.Backend("door", time.Hour)
	again, _ := o.Backend("door", time.Hour)
	b, _ := o.Backend("accounts", time.Hour)
	if a != again || a == b {
		t.Fatal("one purpose is one backend and two purposes are two")
	}
}

func TestOpenerNATS(t *testing.T) {
	env := natstest.Start(t, natstest.Options{JetStream: true})
	cfg := tokenstore.Config{Backend: tokenstore.BackendNATS, NATS: env.Config}
	o, err := cfg.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	be, err := o.Backend("door", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.Put(context.Background(), "g", "n", []byte("v"), time.Hour, tokenstore.MustNotExist); err != nil {
		t.Fatal(err)
	}
	bad := tokenstore.Config{Backend: tokenstore.BackendNATS, NATS: env.Config}
	bad.NATS.URL = "nats://127.0.0.1:1"
	if _, err := bad.Open(); err == nil {
		t.Fatal("an unreachable server must fail at start")
	}
}

func TestOpenerOpenBao(t *testing.T) {
	_, srv := newFakeBao(t, "tok")
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := tokenstore.Config{Backend: tokenstore.BackendOpenBao, OpenBaoAddr: srv.URL, OpenBaoTokenFile: tokenFile}
	o, _ := cfg.Open()
	if _, err := o.Backend("accounts", time.Hour); err == nil {
		t.Fatal("an http address must be refused")
	}
	badCA := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(badCA, []byte("not pem"), 0o600)
	cfg.OpenBaoCAFile = badCA
	o, _ = cfg.Open()
	if _, err := o.Backend("accounts", time.Hour); err == nil || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("a CA file with no certificate: %v", err)
	}
	cfg.OpenBaoCAFile = filepath.Join(dir, "missing")
	o, _ = cfg.Open()
	if _, err := o.Backend("accounts", time.Hour); err == nil {
		t.Fatal("a missing CA file")
	}
}
