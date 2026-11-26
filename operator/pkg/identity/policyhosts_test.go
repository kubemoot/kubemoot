/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/netguard"
)

// httptest's certificate is valid for this name and for 127.0.0.1.
const (
	pdpName    = "example.com"
	pdpSubject = "alice"
)

func TestParsePolicyHostsKeepsPlainHostNamesOnly(t *testing.T) {
	hosts, rejected := ParsePolicyHosts(" PDP.example.org, ,opa.internal.example. ,https://x.example,y.example:8443,*.example.org,a/b")
	if len(hosts) != 2 || hosts[0] != "pdp.example.org" || hosts[1] != "opa.internal.example" {
		t.Errorf("hosts = %v", hosts)
	}
	if len(rejected) != 4 {
		t.Errorf("rejected = %v", rejected)
	}
}

func TestPolicyHostsTrustOnlyHttpsAddressesOnTheList(t *testing.T) {
	hosts, _ := ParsePolicyHosts("pdp.example.org")
	for raw, want := range map[string]bool{
		"https://pdp.example.org/access/v1/evaluation":      true,
		"https://PDP.example.org:443/x":                     true,
		"https://pdp.example.org:8443/x":                    false,
		"https://pdp.example.org:80/x":                      false,
		"http://pdp.example.org/x":                          false,
		"https://other.example.org/x":                       false,
		"https://pdp.example.org.evil.example/x":            false,
		"https://evil.example@pdp.example.org/x":            false,
		"https://pdp.example.org@evil.example/x":            false,
		"https://10.0.0.1/x":                                false,
		"https://kubemoot-nats.kubemoot.svc.cluster.local/": false,
		"": false,
	} {
		if got := hosts.Trusts(raw); got != want {
			t.Errorf("Trusts(%q) = %v, want %v", raw, got, want)
		}
	}
	if (PolicyHosts{}).Trusts("https://pdp.example.org/x") {
		t.Error("an empty list trusted a host")
	}
}

// answeringPDP is a policy decision point that always says yes and counts the calls it gets.
func answeringPDP(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirected" {
			_, _ = w.Write([]byte(`{"decision":true}`))
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte(`{"decision":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// guardFor trusts pdpName and sends every dial for it to srv, whatever Allow says of the address.
func guardFor(srv *httptest.Server, answers func() []netip.Addr, allow func(netip.Addr) bool) PolicyGuard {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return PolicyGuard{
		Hosts: PolicyHosts{pdpName},
		Dialer: &netguard.Dialer{
			Lookup: func(context.Context, string) ([]netip.Addr, error) { return answers(), nil },
			Allow:  allow,
		},
		TLS: &tls.Config{RootCAs: pool, ServerName: pdpName, MinVersion: tls.VersionTLS12},
	}
}

// pdpURL is an address on the default port, 443, as a rule must be. The test server listens
// elsewhere, so servedBy sends the connection there after the guard has checked the address.
func pdpURL(_ *httptest.Server, path string) string { return "https://" + pdpName + path }

// servedBy makes the client's connections to port 443 reach srv; the guard still resolves and checks first.
func servedBy(c *AuthZENClient, srv *httptest.Server) *AuthZENClient {
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	tr := c.client.Transport.(*http.Transport)
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		return dial(ctx, network, net.JoinHostPort(host, port))
	}
	return c
}

// guardedClient is the guarded client for cfg, with its connections sent to srv.
func guardedClient(srv *httptest.Server, cfg ExternalConfig, g PolicyGuard) (*AuthZENClient, error) {
	c, err := NewGuardedAuthZENClient(cfg, "", g)
	if err != nil {
		return nil, err
	}
	return servedBy(c, srv), nil
}

func evaluate(c *AuthZENClient) (bool, error) {
	return c.Evaluate(context.Background(), Identity{Issuer: "hop:door", Subject: pdpSubject}, "ns/crew")
}

var loopback = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }

func TestGuardedClientRefusesAHostTheInstallDoesNotList(t *testing.T) {
	srv, calls := answeringPDP(t)
	g := guardFor(srv, loopback, func(netip.Addr) bool { return true })
	g.Hosts = PolicyHosts{"another.example.org"}
	if _, err := NewGuardedAuthZENClient(ExternalConfig{URL: pdpURL(srv, "/")}, "", g); !errors.Is(err, ErrPolicyHostNotTrusted) {
		t.Fatalf("err = %v, want ErrPolicyHostNotTrusted", err)
	}
	g.Hosts = nil
	if _, err := NewGuardedAuthZENClient(ExternalConfig{URL: pdpURL(srv, "/")}, "", g); !errors.Is(err, ErrPolicyHostNotTrusted) {
		t.Fatalf("an empty list: err = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("the policy decision point was called")
	}
}

func TestGuardedClientCallsAListedHost(t *testing.T) {
	srv, calls := answeringPDP(t)
	g := guardFor(srv, loopback, func(netip.Addr) bool { return true })
	c, err := guardedClient(srv, ExternalConfig{URL: pdpURL(srv, "/access/v1/evaluation")}, g)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := evaluate(c); err != nil || !ok || calls.Load() != 1 {
		t.Fatalf("ok=%v err=%v calls=%d", ok, err, calls.Load())
	}
}

func TestGuardedClientNeverConnectsToAPrivateAddress(t *testing.T) {
	srv, calls := answeringPDP(t)
	for name, answers := range map[string][]netip.Addr{
		"private":               {netip.MustParseAddr("10.1.2.3")},
		"loopback":              {netip.MustParseAddr("127.0.0.1")},
		"link local":            {netip.MustParseAddr("169.254.169.254")},
		"cluster service range": {netip.MustParseAddr("100.64.0.9")},
		"mapped loopback":       {netip.MustParseAddr("::ffff:127.0.0.1")},
		"one bad among good":    {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
	} {
		g := guardFor(srv, func() []netip.Addr { return answers }, netguard.PublicAddr)
		c, err := guardedClient(srv, ExternalConfig{URL: pdpURL(srv, "/")}, g)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := evaluate(c); ok || err == nil {
			t.Errorf("%s: ok=%v err=%v, want a refusal", name, ok, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("a private address was reached (%d calls)", calls.Load())
	}
}

func TestGuardedClientIsNotRebound(t *testing.T) {
	srv, calls := answeringPDP(t)
	var asked atomic.Int32
	// The name answers a public address the first time and a private one after, as a rebinding
	// attack does; the second connection checks the new answer and is refused.
	answers := func() []netip.Addr {
		if asked.Add(1) == 1 {
			return loopback()
		}
		return []netip.Addr{netip.MustParseAddr("192.168.1.10")}
	}
	allowFirstOnly := func(a netip.Addr) bool { return a.IsLoopback() || netguard.PublicAddr(a) }
	g := guardFor(srv, answers, allowFirstOnly)
	c, err := guardedClient(srv, ExternalConfig{URL: pdpURL(srv, "/")}, g)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := evaluate(c); err != nil || !ok {
		t.Fatalf("first call: ok=%v err=%v", ok, err)
	}
	if ok, err := evaluate(c); ok || err == nil {
		t.Fatalf("after the name changed its answer: ok=%v err=%v, want a refusal", ok, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestGuardedClientDoesNotFollowARedirect(t *testing.T) {
	srv, calls := answeringPDP(t)
	g := guardFor(srv, loopback, func(netip.Addr) bool { return true })
	c, err := guardedClient(srv, ExternalConfig{URL: pdpURL(srv, "/redirect")}, g)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := evaluate(c); ok || err == nil {
		t.Fatalf("a redirect admitted: ok=%v err=%v", ok, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d: the redirect target was called", calls.Load())
	}
}

func TestFromEnvRefusesEveryoneForAnExternalRuleWithAnUnlistedHost(t *testing.T) {
	rule := `{"external":{"url":"https://pdp.example.org/x"},"namespace":"ns","crew":"pilot"}`
	for hosts, wantPDP := range map[string]bool{"": false, "other.example.org": false, "pdp.example.org": true} {
		env := map[string]string{EnvHashKey: "k", EnvAskersPolicyHosts: hosts}
		a, err := askerAuthorizerFromEnv(func(k string) string { return env[k] }, mustRule(t, rule), Hasher{Key: []byte("k")})
		if err != nil {
			t.Fatal(err)
		}
		if got := a.(AskerAuthorizer).PDP != nil; got != wantPDP {
			t.Errorf("hosts %q: evaluator set = %v, want %v", hosts, got, wantPDP)
		}
		ok, err := a.Allowed(context.Background(), &Identity{Issuer: HopIssuerPrefix + "door", Subject: pdpSubject}, UseAccess("ns", "pilot"))
		if !wantPDP && (ok || err != nil) {
			t.Errorf("hosts %q: ok=%v err=%v, want a plain refusal", hosts, ok, err)
		}
	}
}

func mustRule(t *testing.T, raw string) AskersConfig {
	t.Helper()
	r, err := ParseAskers(raw)
	if err != nil {
		t.Fatal(err)
	}
	return *r
}

func TestGuardedClientRefusesAListedHostOnAnotherPort(t *testing.T) {
	srv, calls := answeringPDP(t)
	g := guardFor(srv, loopback, func(netip.Addr) bool { return true })
	if _, err := NewGuardedAuthZENClient(ExternalConfig{URL: "https://" + pdpName + ":8443/x"}, "", g); !errors.Is(err, ErrPolicyHostNotTrusted) {
		t.Fatalf("err = %v, want ErrPolicyHostNotTrusted", err)
	}
	if calls.Load() != 0 {
		t.Fatal("the policy decision point was called")
	}
}
