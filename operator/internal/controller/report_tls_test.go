/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	reportTestService = "k-report"
	reportTestHost    = "localhost"
)

func reportNames() []string { return serviceDNSNames(reportTestService, caTestNS) }

func verifies(t *testing.T, c *tls.Certificate, roots []*x509.Certificate, name string, now time.Time) error {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: poolOf(roots), CurrentTime: now})
	return err
}

func TestServingCertificateIsSignedByTheInstallCAForTheServiceNames(t *testing.T) {
	f := newCAFixture(t)
	get := f.ca.ServingCertificate(reportNames())
	c, err := get(nil)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := f.ca.Authority(context.Background())
	for _, name := range reportNames() {
		if err := verifies(t, c, []*x509.Certificate{ca.cert}, name, f.now); err != nil {
			t.Errorf("does not verify for %s: %v", name, err)
		}
	}
	if verifies(t, c, []*x509.Certificate{ca.cert}, "other.ns.svc", f.now) == nil {
		t.Error("verifies for a name it was not issued for")
	}
	again, _ := get(nil)
	if again != c {
		t.Error("a fresh certificate was issued again")
	}
}

func TestServingCertificateFollowsARotationAndRenewal(t *testing.T) {
	f := newCAFixture(t)
	get := f.ca.ServingCertificate(reportNames())
	first, _ := get(nil)
	f.rotate(t)
	f.now = f.now.Add(servingRecheck)
	second, err := get(nil)
	if err != nil || second == first {
		t.Fatalf("a certificate the new CA did not sign was kept: %v", err)
	}
	newCA, _ := f.ca.Authority(context.Background())
	if err := verifies(t, second, []*x509.Certificate{newCA.cert}, reportNames()[2], f.now); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(leafLifetime * 3 / 4)
	if third, _ := get(nil); third == second {
		t.Fatal("a certificate in the last third of its life was kept")
	}
}

func TestServingCertificateKeepsServingWhileTheCAIsUnreadableAndFailsWithNoCertificate(t *testing.T) {
	f := newCAFixture(t)
	get := f.ca.ServingCertificate(reportNames())
	first, err := get(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(servingRecheck)
	s := f.secret(t)
	s.Data[corev1.TLSPrivateKeyKey] = []byte("garbage")
	if err := f.ca.Client.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got, err := get(nil); err != nil || got != first {
		t.Fatalf("a valid certificate was dropped on an unreadable CA: %v", err)
	}
	empty := newCAFixture(t)
	empty.ca.SecretName = "no-such-ca-and-no-create"
	empty.ca.Client = failingCreate(t, empty)
	if _, err := empty.ca.ServingCertificate(reportNames())(nil); err == nil {
		t.Fatal("a certificate was served with no CA and none cached")
	}
}

func TestServingCertificateRefusesAnExpiringCA(t *testing.T) {
	f := newCAFixture(t)
	get := f.ca.ServingCertificate(reportNames())
	if _, err := get(nil); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(caLifetime)
	if got, err := get(nil); err == nil && got.Leaf.NotAfter.After(f.now) {
		t.Fatalf("an expired CA's certificate was served past its end")
	}
	f.rotate(t)
	f.now = f.now.Add(caLifetime)
	if _, err := get(nil); err == nil {
		t.Fatal("a certificate was issued from an expired CA")
	}
}

func TestReportCertificateNeedsACAAndAServiceName(t *testing.T) {
	f := newCAFixture(t)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if ReportCertificate(nil, env(map[string]string{envReportService: "s"})) != nil {
		t.Fatal("a certificate source with no CA")
	}
	if ReportCertificate(f.ca, env(nil)) != nil {
		t.Fatal("a certificate source with no Service name")
	}
	if ReportCertificate(f.ca, env(map[string]string{envReportService: "s"})) == nil {
		t.Fatal("no certificate source with both")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func TestTheReportServiceServesTLSToAClientThatTrustsTheCAAndRefusesOthers(t *testing.T) {
	f := newCAFixture(t)
	addr := freeAddr(t)
	srv := &ReportServer{Addr: addr, Certificate: f.ca.ServingCertificate([]string{reportTestHost})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	ca, _ := f.ca.Authority(context.Background())
	trusting := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: poolOf([]*x509.Certificate{ca.cert}), MinVersion: tls.VersionTLS12}}}
	url := "https://" + net.JoinHostPort(reportTestHost, portOf(addr)) + "/componentstatuses"
	var resp *http.Response
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if resp, err = trusting.Get(url); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("a client that trusts the CA: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	other := newCAFixture(t)
	otherCA, _ := other.ca.Authority(context.Background())
	untrusting := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: poolOf([]*x509.Certificate{otherCA.cert}), MinVersion: tls.VersionTLS12}}}
	if _, err := untrusting.Get(url); err == nil {
		t.Fatal("a client that does not trust the CA was served")
	}
	if r, err := http.Get("http://" + addr + "/componentstatuses"); err == nil {
		_ = r.Body.Close()
		if r.StatusCode < 400 {
			t.Fatal("plain http answered on the TLS port")
		}
	}
}

func portOf(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

// failingCreate is a client whose reads find nothing and whose writes fail, as an API server
// that refuses the operator would be.
func failingCreate(t *testing.T, f *caFixture) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(f.scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return errors.New("forbidden")
		},
	}).Build()
}

func TestAFreshCertificateIsServedWithoutReadingTheCAOnEveryHandshake(t *testing.T) {
	f := newCAFixture(t)
	reads := 0
	f.ca.Client = countingReads(t, f, &reads)
	get := f.ca.ServingCertificate(reportNames())
	for range 5 {
		if _, err := get(nil); err != nil {
			t.Fatal(err)
		}
	}
	if reads > 3 {
		t.Fatalf("the CA Secret was read %d times for five handshakes", reads)
	}
}

func countingReads(t *testing.T, f *caFixture, reads *int) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(f.scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				*reads++
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
}

func TestDuringAnOutageTheCAIsNotReadOnEveryHandshake(t *testing.T) {
	f := newCAFixture(t)
	get := f.ca.ServingCertificate(reportNames())
	first, err := get(nil)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	f.ca.Client = failingReads(t, f, &reads)
	f.now = f.now.Add(servingRecheck)
	for range 5 {
		if got, err := get(nil); err != nil || got != first {
			t.Fatalf("the held certificate was not served through the outage: %v", err)
		}
	}
	if reads != 1 {
		t.Fatalf("the CA Secret was read %d times during the outage, want one attempt", reads)
	}
}

func failingReads(t *testing.T, f *caFixture, reads *int) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(f.scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			*reads++
			return errors.New("the API server is not reachable")
		},
	}).Build()
}

func TestTheReportServiceRefusesToStartWithoutACertificateUnlessToldTo(t *testing.T) {
	srv := &ReportServer{Addr: freeAddr(t)}
	if err := srv.Start(context.Background()); !errors.Is(err, errReportNeedsTLS) {
		t.Fatalf("Start with no certificate: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	opted := &ReportServer{Addr: freeAddr(t), AllowInsecure: true}
	done := make(chan error, 1)
	go func() { done <- opted.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an explicit opt-in did not start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not stop")
	}
}
