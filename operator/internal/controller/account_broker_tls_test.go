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
	"crypto/x509"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	brokerTestService = "kubemoot-liaison-broker"
	brokerTestSecret  = "kubemoot-liaison-broker-tls"
	brokerHTTPSURL    = "https://kubemoot-liaison-broker.kubemoot.svc:8081"
	brokerCAPath      = "/etc/kubemoot/gateway-tls/ca-bundle.crt"
)

func brokerFixture(t *testing.T) (*caFixture, *BrokerCertificate) {
	t.Helper()
	f := newCAFixture(t)
	return f, &BrokerCertificate{CA: f.ca, Service: brokerTestService, SecretName: brokerTestSecret}
}

func brokerLeaf(t *testing.T, f *caFixture) (*corev1.Secret, *x509.Certificate) {
	t.Helper()
	s := &corev1.Secret{}
	key := client.ObjectKey{Namespace: caTestNS, Name: brokerTestSecret}
	if err := f.ca.Client.Get(context.Background(), key, s); err != nil {
		t.Fatal(err)
	}
	leaf, err := tlsKeyPair(s.Data)
	if err != nil {
		t.Fatal(err)
	}
	return s, leaf
}

func TestBrokerCertificateNeedsACAAndTheChartsNames(t *testing.T) {
	f := newCAFixture(t)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	full := map[string]string{envBrokerTLSSecret: brokerTestSecret, envBrokerService: brokerTestService}
	if BrokerCertificateFromEnv(nil, env(full)) != nil {
		t.Error("an issuer with no CA")
	}
	for name, m := range map[string]map[string]string{
		"no names": nil, "no service": {envBrokerTLSSecret: "s"}, "no secret": {envBrokerService: "s"},
	} {
		if BrokerCertificateFromEnv(f.ca, env(m)) != nil {
			t.Errorf("%s: an issuer", name)
		}
	}
	b := BrokerCertificateFromEnv(f.ca, env(full))
	if b == nil || b.Service != brokerTestService || b.SecretName != brokerTestSecret {
		t.Fatalf("the issuer: %+v", b)
	}
}

func TestBrokerCertificateNamesTheBrokerServiceAndCarriesTheCA(t *testing.T) {
	f, b := brokerFixture(t)
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, leaf := brokerLeaf(t, f)
	if err := leaf.VerifyHostname(brokerTestService + "." + caTestNS + ".svc"); err != nil {
		t.Errorf("the Service's DNS name is not on the certificate: %v", err)
	}
	ca, _ := f.ca.Authority(context.Background())
	if string(s.Data[secretKeyCACert]) != string(ca.pem) || s.Labels[LabelInstallCALeaf] != valueTrue {
		t.Errorf("the Secret: labels %v", s.Labels)
	}
}

func TestBrokerCertificateIsReissuedWhenTheCAChanges(t *testing.T) {
	f, b := brokerFixture(t)
	ctx := context.Background()
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	_, before := brokerLeaf(t, f)
	f.rotate(t)
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	_, after := brokerLeaf(t, f)
	if before.SerialNumber.Cmp(after.SerialNumber) == 0 {
		t.Fatal("the certificate was kept after the CA changed")
	}
}

func TestBrokerCertificateRunsUntilStopped(t *testing.T) {
	f, b := brokerFixture(t)
	b.Every = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := f.ca.Client.Get(ctx, client.ObjectKey{Namespace: caTestNS, Name: brokerTestSecret}, &corev1.Secret{}); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	brokerLeaf(t, f)
}

func TestAnHTTPSBrokerComesWithTheCAFileAndAnHTTPOneDoesNot(t *testing.T) {
	crew := connectedAccountCrew(t)
	pod := gatewayPod()
	applyAccountBroker(pod, crew, brokerHTTPSURL, brokerCAPath)
	if v, _ := envValue(pod.Containers[0].Env, identity.EnvAccountBrokerCAFile); v != brokerCAPath {
		t.Errorf("the CA file of an https broker: %q", v)
	}
	if v, _ := envValue(pod.Containers[0].Env, identity.EnvAccountBrokerURL); v != brokerHTTPSURL {
		t.Errorf("the broker address: %q", v)
	}
	plain := gatewayPod()
	applyAccountBroker(plain, crew, brokerURL, brokerCAPath)
	if _, ok := envValue(plain.Containers[0].Env, identity.EnvAccountBrokerCAFile); ok {
		t.Error("an http broker was given a CA file")
	}
}

func TestTheBrokerCAFileIsTheGatewayCertificatesCAOnlyWithGatewayTLS(t *testing.T) {
	r := &CrewReconciler{}
	if got := r.brokerCAFile(); got != "" {
		t.Errorf("without gateway TLS: %q", got)
	}
	r.GatewayTLS = &GatewayTLS{}
	if got := r.brokerCAFile(); got != brokerCAPath {
		t.Errorf("with gateway TLS: %q", got)
	}
}

// bundleOf is the CA bundle the gateway-side leaf Secret carries.
func bundleOf(t *testing.T, f *caFixture) []*x509.Certificate {
	t.Helper()
	s, _ := f.leaf(t)
	return certificatesIn(s.Data[secretKeyCABundle])
}

func TestAGatewayTrustsTheBrokerThroughACARotation(t *testing.T) {
	f, b := brokerFixture(t)
	ctx := context.Background()
	ensureGateway := func() {
		t.Helper()
		if _, err := f.ca.EnsureLeaf(ctx, leafKey(), []string{caTestHost}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	ensureGateway()
	if n := len(bundleOf(t, f)); n != 1 {
		t.Fatalf("before a rotation the bundle holds %d certificates", n)
	}
	_, oldBroker := brokerLeaf(t, f)

	// The CA changes and the gateway's certificate is issued first: the broker still serves a
	// certificate the previous CA signed, so the bundle keeps that CA.
	f.rotate(t)
	ensureGateway()
	pool := x509.NewCertPool()
	for _, c := range bundleOf(t, f) {
		pool.AddCert(c)
	}
	if _, err := oldBroker.Verify(x509.VerifyOptions{
		Roots: pool, DNSName: brokerTestService + "." + caTestNS + ".svc", CurrentTime: f.now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Fatalf("the gateway cannot verify the broker's previous certificate in the rotation window: %v", err)
	}

	// The broker is re-issued; the next check of the gateway's Secret drops the previous CA, with
	// no new certificate for the gateway.
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	_, before := f.leaf(t)
	ensureGateway()
	if _, after := f.leaf(t); before.SerialNumber.Cmp(after.SerialNumber) != 0 {
		t.Error("the gateway's certificate was replaced only to refresh the bundle")
	}
	if n := len(bundleOf(t, f)); n != 1 {
		t.Fatalf("after the broker is re-issued the bundle holds %d certificates", n)
	}
}
