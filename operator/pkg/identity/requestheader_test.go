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
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubemoot/kubemoot/operator/internal/identitytest"
)

const (
	proxyCN      = "front-proxy-client"
	userHeader   = "X-Remote-User"
	groupHeader  = "X-Remote-Group"
	extraPrefix  = "X-Remote-Extra-"
	forgedUser   = "system:admin"
	testUser     = "jane"
	allowedNames = `["front-proxy-client"]`
)

func frontProxyData(ca *identitytest.CA, allowed string) map[string]string {
	return map[string]string{
		keyRequestHeaderCA:        string(ca.PEM),
		keyRequestHeaderAllowed:   allowed,
		keyRequestHeaderUsernames: `["X-Remote-User"]`,
		keyRequestHeaderGroups:    `["X-Remote-Group"]`,
		keyRequestHeaderExtra:     `["X-Remote-Extra-"]`,
	}
}

func trustOf(t *testing.T, ca *identitytest.CA, allowed string) *RequestHeaderTrust {
	t.Helper()
	cfg, err := ParseFrontProxyConfig(frontProxyData(ca, allowed))
	if err != nil {
		t.Fatal(err)
	}
	return NewRequestHeaderTrust(cfg)
}

// proxied builds a request as the aggregator sends it: a verified certificate and the headers.
func proxied(cert *tls.Certificate) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/apis/g/v/x", nil)
	r.Header.Set(userHeader, testUser)
	r.Header.Add(groupHeader, "dev")
	r.Header.Add(groupHeader, "system:authenticated")
	r.Header.Set(extraPrefix+"Scopes", "a")
	r.Header.Add(extraPrefix+"Scopes", "b")
	r.Header.Set(extraPrefix+"Example.com%2Fdept", "ops")
	if cert != nil {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert.Leaf}}
	}
	return r
}

func farFuture() time.Time { return time.Now().Add(time.Hour) }

func TestRequestHeaderVerifierReadsTheCallerFromAVerifiedProxy(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	cert := ca.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	id, err := RequestHeaderVerifier{Trust: trustOf(t, ca, allowedNames)}.Verify(proxied(&cert))
	if err != nil {
		t.Fatal(err)
	}
	want := &Identity{
		Issuer: IssuerKubernetes, Subject: testUser, Groups: []string{"dev", "system:authenticated"},
		Extra: map[string][]string{"scopes": {"a", "b"}, "example.com/dept": {"ops"}},
	}
	if !reflect.DeepEqual(id, want) {
		t.Errorf("identity = %#v, want %#v", id, want)
	}
}

func TestRequestHeaderVerifierIgnoresHeadersWithoutAVerifiedCertificate(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	rogue := identitytest.NewCA(t, "rogue-ca")
	otherName := ca.Issue(t, "someone-else", x509.ExtKeyUsageClientAuth, farFuture())
	wrongCA := rogue.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	serverUse := ca.Issue(t, proxyCN, x509.ExtKeyUsageServerAuth, farFuture())
	expired := ca.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, time.Now().Add(-time.Minute))
	plain := proxied(nil) // plain HTTP: no TLS state at all
	noCert := proxied(nil)
	noCert.TLS = &tls.ConnectionState{}
	cases := map[string]*http.Request{
		"plain HTTP":                  plain,
		"TLS without a certificate":   noCert,
		"a certificate of another CA": proxied(&wrongCA),
		"a name that is not allowed":  proxied(&otherName),
		"a server-use certificate":    proxied(&serverUse),
		"an expired certificate":      proxied(&expired),
	}
	v := RequestHeaderVerifier{Trust: trustOf(t, ca, allowedNames)}
	for name, r := range cases {
		r.Header.Set(userHeader, forgedUser)
		if id, err := v.Verify(r); !errors.Is(err, ErrNoCredential) || id != nil {
			t.Errorf("%s: identity = %v, err = %v; want no credential", name, id, err)
		}
	}
}

func TestRequestHeaderVerifierWithNoAllowedNamesTrustsAnyCertOfTheCA(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	cert := ca.Issue(t, "any-name", x509.ExtKeyUsageClientAuth, farFuture())
	if _, err := (RequestHeaderVerifier{Trust: trustOf(t, ca, "")}).Verify(proxied(&cert)); err != nil {
		t.Errorf("empty allowed names should admit the CA's certificates: %v", err)
	}
	rogue := identitytest.NewCA(t, "rogue-ca")
	bad := rogue.Issue(t, "any-name", x509.ExtKeyUsageClientAuth, farFuture())
	if _, err := (RequestHeaderVerifier{Trust: trustOf(t, ca, "")}).Verify(proxied(&bad)); !errors.Is(err, ErrNoCredential) {
		t.Errorf("a certificate of another CA must never be trusted, err = %v", err)
	}
}

func TestRequestHeaderVerifierRefusesAProxyRequestWithNoUser(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	cert := ca.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	r := proxied(&cert)
	r.Header.Del(userHeader)
	if _, err := (RequestHeaderVerifier{Trust: trustOf(t, ca, "")}).Verify(r); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestRequestHeaderVerifierTrustsNothingBeforeTheConfigurationIsLoaded(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	cert := ca.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	_, err := RequestHeaderVerifier{Trust: NewRequestHeaderTrust(nil)}.Verify(proxied(&cert))
	if !errors.Is(err, ErrFrontProxyNotLoaded) {
		t.Errorf("err = %v, want ErrFrontProxyNotLoaded", err)
	}
}

func TestRequestHeaderVerifierHonorsTheConfiguredHeaderNames(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	data := frontProxyData(ca, "")
	data[keyRequestHeaderUsernames] = `["X-Other-User","X-Remote-User"]`
	cfg, err := ParseFrontProxyConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	cert := ca.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	r := proxied(&cert)
	r.Header.Set("X-Other-User", "first")
	id, err := RequestHeaderVerifier{Trust: NewRequestHeaderTrust(cfg)}.Verify(r)
	if err != nil || id.Subject != "first" {
		t.Errorf("identity = %v, err = %v; want the first named header", id, err)
	}
}

func TestParseFrontProxyConfigRefusesWhatCannotBeTrusted(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	for name, mutate := range map[string]func(map[string]string){
		"no CA":               func(d map[string]string) { delete(d, keyRequestHeaderCA) },
		"a CA that is no PEM": func(d map[string]string) { d[keyRequestHeaderCA] = "not a certificate" },
		"no username header":  func(d map[string]string) { d[keyRequestHeaderUsernames] = "" },
		"a malformed list":    func(d map[string]string) { d[keyRequestHeaderAllowed] = "front-proxy-client" },
	} {
		d := frontProxyData(ca, allowedNames)
		mutate(d)
		if _, err := ParseFrontProxyConfig(d); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func frontProxyClient(ca *identitytest.CA) *fake.Clientset {
	return fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: FrontProxyConfigNamespace, Name: FrontProxyConfigName},
		Data:       frontProxyData(ca, allowedNames),
	})
}

func TestRequestHeaderTrustLoadsTheClusterConfigMapAndKeepsTheLastGoodOne(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	trust := NewRequestHeaderTrust(nil)
	if trust.Config() != nil {
		t.Fatal("a new trust must hold nothing")
	}
	if err := trust.Load(context.Background(), frontProxyClient(ca)); err != nil || trust.Config() == nil {
		t.Fatalf("load: %v", err)
	}
	good := trust.Config()
	if err := trust.Load(context.Background(), fake.NewSimpleClientset()); err == nil {
		t.Error("a missing ConfigMap must be an error")
	}
	if trust.Config() != good {
		t.Error("a failed read must keep the last good configuration")
	}
}

// handshakeServer serves the verifier over TLS the way the listener does.
func handshakeServer(t *testing.T, trust *RequestHeaderTrust, serverCA *identitytest.CA) *httptest.Server {
	t.Helper()
	server := serverCA.Issue(t, "localhost", x509.ExtKeyUsageServerAuth, farFuture())
	server.Leaf.DNSNames = nil
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := RequestHeaderVerifier{Trust: trust}.Verify(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(id.Subject))
	}))
	srv.TLS = trust.ServerTLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &server, nil }, false)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func callWith(t *testing.T, srv *httptest.Server, cert *tls.Certificate) (string, error) {
	t.Helper()
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // the test server's own certificate
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set(userHeader, testUser)
	resp, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n]), nil
}

func TestServerTLSConfigRefusesConnectionsThatAreNotTheAPIServer(t *testing.T) {
	ca, rogue := identitytest.NewCA(t, "front-proxy-ca"), identitytest.NewCA(t, "rogue-ca")
	srv := handshakeServer(t, trustOf(t, ca, allowedNames), identitytest.NewCA(t, "serving-ca"))
	good := ca.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	if body, err := callWith(t, srv, &good); err != nil || body != testUser {
		t.Errorf("the API server's certificate: body = %q, err = %v", body, err)
	}
	bad := rogue.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	if _, err := callWith(t, srv, &bad); err == nil {
		t.Error("a certificate of another CA must fail the handshake")
	}
	if _, err := callWith(t, srv, nil); err == nil {
		t.Error("a client with no certificate must fail the handshake")
	}
}

func TestServerTLSConfigFailsTheHandshakeBeforeTheCAIsLoaded(t *testing.T) {
	ca := identitytest.NewCA(t, "front-proxy-ca")
	srv := handshakeServer(t, NewRequestHeaderTrust(nil), identitytest.NewCA(t, "serving-ca"))
	good := ca.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	if _, err := callWith(t, srv, &good); err == nil {
		t.Error("with no CA loaded no client may connect")
	}
}

func TestServerTLSConfigFollowsARotatedCA(t *testing.T) {
	oldCA, newCA := identitytest.NewCA(t, "old-ca"), identitytest.NewCA(t, "new-ca")
	trust := trustOf(t, oldCA, "")
	srv := handshakeServer(t, trust, identitytest.NewCA(t, "serving-ca"))
	fromNew := newCA.Issue(t, proxyCN, x509.ExtKeyUsageClientAuth, farFuture())
	if _, err := callWith(t, srv, &fromNew); err == nil {
		t.Fatal("before the rotation the new CA's certificate must fail")
	}
	if err := trust.Load(context.Background(), frontProxyClient(newCA)); err != nil {
		t.Fatal(err)
	}
	if body, err := callWith(t, srv, &fromNew); err != nil || body != testUser {
		t.Errorf("after the rotation: body = %q, err = %v", body, err)
	}
}
