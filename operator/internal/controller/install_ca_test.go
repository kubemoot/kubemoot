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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	caTestNS        = "kubemoot"
	caTestSecret    = "kubemoot-ca"
	caTestConfigMap = "kubemoot-ca-bundle"
	caTestHost      = "svc.ns.svc"
	caTestLeafNS    = "leaf-ns"
	caTestOtherHost = "other.ns.svc"
)

// caFixture is an InstallCA over a fake client, with a clock the test moves.
type caFixture struct {
	ca     *InstallCA
	scheme *runtime.Scheme
	now    time.Time
}

func newCAFixture(t *testing.T, objs ...client.Object) *caFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	f := &caFixture{scheme: scheme, now: time.Now()}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	f.ca = &InstallCA{Client: c, Namespace: caTestNS, SecretName: caTestSecret, ConfigMap: caTestConfigMap, Now: func() time.Time { return f.now }}
	return f
}

func (f *caFixture) secret(t *testing.T) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := f.ca.Client.Get(context.Background(), client.ObjectKey{Namespace: caTestNS, Name: caTestSecret}, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *caFixture) bundle(t *testing.T) []*x509.Certificate {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := f.ca.Client.Get(context.Background(), client.ObjectKey{Namespace: caTestNS, Name: caTestConfigMap}, cm); err != nil {
		t.Fatal(err)
	}
	return certificatesIn([]byte(cm.Data[secretKeyCACert]))
}

// rotate replaces the CA Secret with a new CA, as an administrator deleting it would.
func (f *caFixture) rotate(t *testing.T) {
	t.Helper()
	data, err := newCASecretData(f.now)
	if err != nil {
		t.Fatal(err)
	}
	s := f.secret(t)
	s.Data = data
	if err := f.ca.Client.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func poolOf(certs []*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool
}

func leafPEM(t *testing.T, a *authority, now time.Time, names ...string) map[string][]byte {
	t.Helper()
	crt, key, err := a.issue(names, now)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key}
}

func TestAuthorityMakesTheCASecretWhenAbsentAndNeverReplacesIt(t *testing.T) {
	f := newCAFixture(t)
	first, err := f.ca.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.secret(t).Type != corev1.SecretTypeTLS {
		t.Fatal("the CA Secret is not a TLS Secret")
	}
	second, err := f.ca.Authority(context.Background())
	if err != nil || !second.cert.Equal(first.cert) {
		t.Fatalf("a second read replaced the CA: %v", err)
	}
}

func TestAuthorityKeepsAnExistingCASecretUntouched(t *testing.T) {
	data, err := newCASecretData(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: caTestSecret, Namespace: caTestNS}, Type: corev1.SecretTypeTLS, Data: data}
	f := newCAFixture(t, existing)
	a, err := f.ca.Authority(context.Background())
	if err != nil || string(a.pem) != string(data[corev1.TLSCertKey]) {
		t.Fatalf("existing CA replaced: %v", err)
	}
}

func TestAuthorityRefusesAnUnreadableMismatchedExpiredOrExpiringCA(t *testing.T) {
	other, _ := newCASecretData(time.Now())
	good, _ := newCASecretData(time.Now())
	cases := map[string]map[string][]byte{
		"unreadable key": {corev1.TLSCertKey: good[corev1.TLSCertKey], corev1.TLSPrivateKeyKey: []byte("garbage")},
		"mismatched key": {corev1.TLSCertKey: good[corev1.TLSCertKey], corev1.TLSPrivateKeyKey: other[corev1.TLSPrivateKeyKey]},
		"not PEM":        {corev1.TLSCertKey: []byte("x"), corev1.TLSPrivateKeyKey: good[corev1.TLSPrivateKeyKey]},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: caTestSecret, Namespace: caTestNS}, Data: data}
			if _, err := newCAFixture(t, s).ca.Authority(context.Background()); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	f := newCAFixture(t)
	if _, err := f.ca.Authority(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(caLifetime - caMinRemaining/2)
	if _, err := f.ca.Authority(context.Background()); !errors.Is(err, errCAExpiring) {
		t.Fatalf("a CA near its end: %v", err)
	}
	f.now = f.now.Add(caLifetime)
	if _, err := f.ca.Authority(context.Background()); !errors.Is(err, errCAExpiring) {
		t.Fatalf("an expired CA: %v", err)
	}
}

func TestACertificateEndsNoLaterThanItsCA(t *testing.T) {
	f := newCAFixture(t)
	a, _ := f.ca.Authority(context.Background())
	late := f.now.Add(caLifetime - caMinRemaining - 24*time.Hour)
	leaf, err := tlsKeyPair(leafPEM(t, a, late, caTestHost))
	if err != nil {
		t.Fatal(err)
	}
	if leaf.NotAfter.After(a.cert.NotAfter) {
		t.Fatalf("leaf ends %v, after the CA at %v", leaf.NotAfter, a.cert.NotAfter)
	}
	if _, _, err := a.issue([]string{caTestHost}, f.now.Add(caLifetime)); !errors.Is(err, errCAExpiring) {
		t.Fatalf("issued from an expired CA: %v", err)
	}
}

func TestFreshJudgesNamesSignerKeyAndAge(t *testing.T) {
	f := newCAFixture(t)
	a, _ := f.ca.Authority(context.Background())
	names := []string{caTestHost, "svc.ns"}
	data := leafPEM(t, a, f.now, names...)
	if !a.fresh(data, names, f.now) {
		t.Fatal("a new certificate is not fresh")
	}
	if a.fresh(data, []string{caTestOtherHost}, f.now) {
		t.Fatal("fresh for other names")
	}
	if a.fresh(data, names, f.now.Add(leafLifetime*3/4)) {
		t.Fatal("fresh in the last third of its life")
	}
	other, _ := newCAFixture(t).ca.Authority(context.Background())
	if other.fresh(data, names, f.now) {
		t.Fatal("fresh for a CA that did not sign it")
	}
	data[corev1.TLSPrivateKeyKey] = []byte("garbage")
	if a.fresh(data, names, f.now) {
		t.Fatal("fresh with a damaged key")
	}
}

func TestPublishCAWritesOnlyPublicCertificatesAndOnlyOnChange(t *testing.T) {
	f := newCAFixture(t)
	ctx := context.Background()
	for range 2 {
		if err := f.ca.PublishCA(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cm := &corev1.ConfigMap{}
	_ = f.ca.Client.Get(ctx, client.ObjectKey{Namespace: caTestNS, Name: caTestConfigMap}, cm)
	if len(cm.Data) != 1 || len(cm.BinaryData) != 0 || len(f.bundle(t)) != 1 {
		t.Fatalf("ConfigMap holds %v", cm.Data)
	}
	if block, _ := pem.Decode([]byte(cm.Data[secretKeyCACert])); block == nil || block.Type != pemCertificate {
		t.Fatal("the bundle is not a certificate")
	}
}

func leafKey() client.ObjectKey { return client.ObjectKey{Namespace: caTestLeafNS, Name: "leaf"} }

func (f *caFixture) leaf(t *testing.T) (*corev1.Secret, *x509.Certificate) {
	t.Helper()
	s := &corev1.Secret{}
	if err := f.ca.Client.Get(context.Background(), leafKey(), s); err != nil {
		t.Fatal(err)
	}
	leaf, err := tlsKeyPair(s.Data)
	if err != nil {
		t.Fatal(err)
	}
	return s, leaf
}

func TestEnsureLeafIssuesALabelledOwnedCertificateCarryingItsCA(t *testing.T) {
	f := newCAFixture(t)
	ctx := context.Background()
	owned := false
	notAfter, err := f.ca.EnsureLeaf(ctx, leafKey(), []string{caTestHost}, func(*corev1.Secret) error { owned = true; return nil })
	if err != nil || !owned {
		t.Fatalf("EnsureLeaf: %v owned=%v", err, owned)
	}
	s, leaf := f.leaf(t)
	ca, _ := f.ca.Authority(ctx)
	if !notAfter.Equal(leaf.NotAfter) || s.Labels[LabelInstallCALeaf] != valueTrue || string(s.Data[secretKeyCACert]) != string(ca.pem) {
		t.Fatalf("notAfter %v labels %v", notAfter, s.Labels)
	}
}

func TestEnsureLeafKeepsAFreshCertificateAndReplacesAStaleOne(t *testing.T) {
	f := newCAFixture(t)
	ctx := context.Background()
	ensure := func(names ...string) {
		t.Helper()
		if _, err := f.ca.EnsureLeaf(ctx, leafKey(), names, nil); err != nil {
			t.Fatal(err)
		}
	}
	ensure(caTestHost)
	_, first := f.leaf(t)
	ensure(caTestHost)
	if _, again := f.leaf(t); again.SerialNumber.Cmp(first.SerialNumber) != 0 {
		t.Fatal("a fresh certificate was replaced")
	}
	f.now = f.now.Add(leafLifetime * 3 / 4)
	ensure(caTestHost)
	if _, renewed := f.leaf(t); renewed.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatal("a certificate in the last third of its life was kept")
	}
	ensure(caTestOtherHost)
	if _, renamed := f.leaf(t); len(renamed.DNSNames) != 1 || renamed.DNSNames[0] != caTestOtherHost {
		t.Fatalf("names %v", renamed.DNSNames)
	}
}

func TestEnsureLeafFailsWhenTheOwnerCannotBeSetOrTheCAIsExpiring(t *testing.T) {
	f := newCAFixture(t)
	if _, err := f.ca.EnsureLeaf(context.Background(), leafKey(), []string{caTestHost}, func(*corev1.Secret) error { return errors.New("no owner") }); err == nil {
		t.Fatal("an owner failure was ignored")
	}
	f.now = f.now.Add(caLifetime)
	if _, err := f.ca.EnsureLeaf(context.Background(), leafKey(), []string{caTestHost}, nil); !errors.Is(err, errCAExpiring) {
		t.Fatalf("an expiring CA: %v", err)
	}
}

func TestARotationKeepsThePreviousCAUntilNoCertificateNeedsIt(t *testing.T) {
	f := newCAFixture(t)
	ctx := context.Background()
	if _, err := f.ca.EnsureLeaf(ctx, leafKey(), []string{caTestHost}, nil); err != nil {
		t.Fatal(err)
	}
	_, oldLeaf := f.leaf(t)
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	f.rotate(t)
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	bundle := f.bundle(t)
	if len(bundle) != 2 {
		t.Fatalf("mid-rotation the bundle holds %d certificates, want the new and the previous", len(bundle))
	}
	if _, err := oldLeaf.Verify(x509.VerifyOptions{DNSName: caTestHost, Roots: poolOf(bundle), CurrentTime: f.now}); err != nil {
		t.Fatalf("a leaf of the old CA does not verify mid-rotation: %v", err)
	}
	if _, err := f.ca.EnsureLeaf(ctx, leafKey(), []string{caTestHost}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	newCA, _ := f.ca.Authority(ctx)
	if got := f.bundle(t); len(got) != 1 || !got[0].Equal(newCA.cert) {
		t.Fatalf("the previous CA stayed after every certificate moved: %d", len(got))
	}
}

func TestALostBundleIsRebuiltFromTheLeavesThatNeedThePreviousCA(t *testing.T) {
	f := newCAFixture(t)
	ctx := context.Background()
	if _, err := f.ca.EnsureLeaf(ctx, leafKey(), []string{caTestHost}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	f.rotate(t)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: caTestConfigMap, Namespace: caTestNS}}
	if err := f.ca.Client.Delete(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	_, oldLeaf := f.leaf(t)
	if _, err := oldLeaf.Verify(x509.VerifyOptions{DNSName: caTestHost, Roots: poolOf(f.bundle(t)), CurrentTime: f.now}); err != nil {
		t.Fatalf("the previous CA was lost with the ConfigMap: %v", err)
	}
}

func TestAnUnusableLeafDoesNotHoldAPreviousCAInTheBundle(t *testing.T) {
	f := newCAFixture(t)
	ctx := context.Background()
	if _, err := f.ca.EnsureLeaf(ctx, leafKey(), []string{caTestHost}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	f.rotate(t)
	s, _ := f.leaf(t)
	s.Data[corev1.TLSPrivateKeyKey] = []byte("garbage")
	if err := f.ca.Client.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.bundle(t); len(got) != 1 {
		t.Fatalf("a broken certificate Secret kept the previous CA: %d", len(got))
	}
}

func TestParsePrivateKeyReadsRSAECAndPKCS8AndRefusesTheRest(t *testing.T) {
	ec, _ := newCASecretData(time.Now())
	if _, err := parsePrivateKey(ec[corev1.TLSPrivateKeyKey]); err != nil {
		t.Fatalf("EC: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	if _, err := parsePrivateKey(pkcs1); err != nil {
		t.Fatalf("PKCS#1: %v", err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	if _, err := parsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatalf("PKCS#8: %v", err)
	}
	for _, bad := range [][]byte{nil, []byte("not pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")})} {
		if _, err := parsePrivateKey(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestPublishCAFailsWhenTheCAIsUnreadable(t *testing.T) {
	bad := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: caTestSecret, Namespace: caTestNS}, Data: map[string][]byte{corev1.TLSCertKey: []byte("x")}}
	if err := newCAFixture(t, bad).ca.PublishCA(context.Background()); err == nil {
		t.Fatal("published an unreadable CA")
	}
}

func TestStartRetriesUntilTheCAIsReadableAndStopsOnCancel(t *testing.T) {
	f := newCAFixture(t)
	var failures atomic.Int32
	f.ca.Client = interceptingClient(t, f, &failures)
	f.ca.PublishEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.ca.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	cm := &corev1.ConfigMap{}
	for time.Now().Before(deadline) {
		if f.ca.Client.Get(ctx, client.ObjectKey{Namespace: caTestNS, Name: caTestConfigMap}, cm) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cm.Data[secretKeyCACert] == "" {
		t.Fatal("the bundle was not published after the first attempt failed")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not stop on cancel")
	}
}

// interceptingClient fails the first read of the CA Secret and lets every later read through.
func interceptingClient(t *testing.T, f *caFixture, failures *atomic.Int32) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(f.scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); !ok && failures.Add(1) == 1 {
				return errors.New("the API server is not reachable")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
}

func TestInstallCAFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if InstallCAFromEnv(nil, env(nil)) != nil {
		t.Fatal("a CA with no Secret named")
	}
	c := InstallCAFromEnv(nil, env(map[string]string{envInstallCASecret: "s", envOperatorNamespace: "km"}))
	if c.Namespace != "km" || c.ConfigMap != "s" || c.NeedLeaderElection() {
		t.Fatalf("got %+v", c)
	}
	c = InstallCAFromEnv(nil, env(map[string]string{envInstallCASecret: "s", envInstallCAConfigMap: "cm"}))
	if c.ConfigMap != "cm" || c.Namespace != defaultOperatorNamespace {
		t.Fatalf("got %+v", c)
	}
}
