package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/liaison"
	"github.com/kubemoot/kubemoot/operator/pkg/catransport"
	"github.com/kubemoot/kubemoot/operator/pkg/catransport/catest"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const brokerLeasePath = "/internal/v1/accounts/lease"

// serveBrokerOn starts the broker server on a free port with the settings in env and returns its
// address. The broker has no ServiceAccount token to accept, so a request that reaches its handler
// is answered 401.
func serveBrokerOn(t *testing.T, env map[string]string) string {
	t.Helper()
	cfg, err := brokerTLSConfig(envMap(env))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	svc := &accounts.Service{Broker: accounts.NewBroker(nil, nil, nil, nil, nil)}
	srv := brokerServer(svc, identity.Reviewer{Audiences: []string{liaison.AccountBrokerAudience}}, nil, port, cfg)
	go func() { _ = serveBroker(srv) }()
	t.Cleanup(func() { _ = srv.Close() })
	addr := "127.0.0.1:" + port
	waitListening(t, addr)
	return addr
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the broker did not start listening")
}

func tlsEnv(s catest.Serving) map[string]string {
	return map[string]string{envBrokerCertFile: s.CertFile, envBrokerKeyFile: s.KeyFile}
}

func post(t *testing.T, client *http.Client, url string) (int, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(`{}`))
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func TestBrokerHandshakeSucceedsWithTheInstallCA(t *testing.T) {
	serving := catest.IssueServing(t, 24*time.Hour, "localhost")
	addr := serveBrokerOn(t, tlsEnv(serving))
	tr, err := catransport.New(serving.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	code, err := post(t, &http.Client{Transport: tr}, "https://"+addr+brokerLeasePath)
	if err != nil || code != http.StatusUnauthorized {
		t.Fatalf("a caller that trusts the install CA reaches the broker's token check: %d %v", code, err)
	}
}

func TestBrokerHandshakeIsRefusedWithAnUnknownCA(t *testing.T) {
	serving := catest.IssueServing(t, 24*time.Hour, "localhost")
	addr := serveBrokerOn(t, tlsEnv(serving))
	tr, err := catransport.New(catest.UnrelatedCA(t))
	if err != nil {
		t.Fatal(err)
	}
	if code, err := post(t, &http.Client{Transport: tr}, "https://"+addr+brokerLeasePath); err == nil {
		t.Fatalf("a caller that does not trust the CA was served (status %d)", code)
	}
}

func TestBrokerOnTLSRefusesPlainHTTP(t *testing.T) {
	serving := catest.IssueServing(t, 24*time.Hour, "localhost")
	addr := serveBrokerOn(t, tlsEnv(serving))
	code, err := post(t, &http.Client{}, "http://"+addr+brokerLeasePath)
	if err == nil && code == http.StatusUnauthorized {
		t.Fatal("a plain http request reached the broker's handler while TLS is on")
	}
	if err == nil && code != http.StatusBadRequest {
		t.Fatalf("a plain http request was answered %d, want it refused", code)
	}
}

func TestBrokerServesPlainHTTPOnlyWhenToldTo(t *testing.T) {
	if _, err := brokerTLSConfig(envMap(nil)); err == nil || !strings.Contains(err.Error(), envAllowInsecureBroker) {
		t.Fatalf("no certificate and no opt-in: %v", err)
	}
	cfg, err := brokerTLSConfig(envMap(map[string]string{envAllowInsecureBroker: "true"}))
	if err != nil || cfg != nil {
		t.Fatalf("the opt-in: %v %v", cfg, err)
	}
	addr := serveBrokerOn(t, map[string]string{envAllowInsecureBroker: "true"})
	if code, err := post(t, &http.Client{}, "http://"+addr+brokerLeasePath); err != nil || code != http.StatusUnauthorized {
		t.Fatalf("the plain listener: %d %v", code, err)
	}
}

func TestBrokerTLSSettingsComeTogether(t *testing.T) {
	serving := catest.IssueServing(t, 24*time.Hour, "localhost")
	for name, env := range map[string]map[string]string{
		"cert only": {envBrokerCertFile: serving.CertFile},
		"key only":  {envBrokerKeyFile: serving.KeyFile},
		"missing":   {envBrokerCertFile: "/none/tls.crt", envBrokerKeyFile: "/none/tls.key"},
		"mismatch":  {envBrokerCertFile: serving.CertFile, envBrokerKeyFile: serving.CAFile},
	} {
		if _, err := brokerTLSConfig(envMap(env)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestBrokerServesARenewedCertificateWithoutARestart(t *testing.T) {
	first := catest.IssueServing(t, 24*time.Hour, "localhost")
	cfg, err := brokerTLSConfig(envMap(tlsEnv(first)))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := cfg.GetCertificate(&tls.ClientHelloInfo{})
	second := catest.IssueServing(t, 48*time.Hour, "localhost")
	for _, f := range [][2]string{{second.CertFile, first.CertFile}, {second.KeyFile, first.KeyFile}} {
		data, _ := os.ReadFile(f[0])
		if err := os.WriteFile(f[1], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	after, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || string(after.Certificate[0]) == string(before.Certificate[0]) {
		t.Fatalf("the renewed certificate was not served: %v", err)
	}
}
