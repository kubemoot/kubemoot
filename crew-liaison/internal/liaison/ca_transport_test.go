package liaison

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pemCertBlock is the PEM block type of a certificate.
const pemCertBlock = "CERTIFICATE"

// serverCertPEM is the PEM of a test server's own certificate, which stands as its CA.
func serverCertPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: pemCertBlock, Bytes: srv.Certificate().Raw})
}

func writeCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(file, serverCertPEM(srv), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// unrelatedCA writes a CA file holding a certificate that signed nothing the test serves.
func unrelatedCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "unrelated"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "unrelated.crt")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: pemCertBlock, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func getVia(t *testing.T, tr http.RoundTripper, url string) (int, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func TestTheCATransportTrustsOnlyTheCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	trusting, err := newCATransport(writeCA(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if code, err := getVia(t, trusting, srv.URL); err != nil || code != http.StatusOK {
		t.Fatalf("a server signed by the trusted CA: %d %v", code, err)
	}
	untrusting, err := newCATransport(unrelatedCA(t))
	if err != nil {
		t.Fatal(err)
	}
	if code, err := getVia(t, untrusting, srv.URL); err == nil {
		t.Fatalf("an untrusted certificate was accepted (status %d)", code)
	}
}

func TestTheCATransportRefusesAMissingOrEmptyCAFile(t *testing.T) {
	if _, err := newCATransport(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("a missing CA file was accepted")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("nothing here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newCATransport(empty); err == nil || !strings.Contains(err.Error(), "no certificate") {
		t.Fatalf("an empty CA file: %v", err)
	}
}

func TestARepublishedCABundleIsTrustedWithoutARestart(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	file := unrelatedCA(t)
	tr, err := newCATransport(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := getVia(t, tr, srv.URL); err == nil {
		t.Fatal("served before the CA was published")
	}
	// A rotation publishes the new CA beside the old one, in one bundle.
	old, _ := os.ReadFile(file)
	if err := os.WriteFile(file, append(old, serverCertPEM(srv)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := getVia(t, tr, srv.URL); err != nil || code != http.StatusOK {
		t.Fatalf("after the bundle changed: %d %v", code, err)
	}
}

func TestTheCATransportFailsClosedWhenTheFileGoesAway(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	file := writeCA(t, srv)
	tr, _ := newCATransport(file)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := getVia(t, tr, srv.URL); err == nil {
		t.Fatal("a call went out with no CA file to verify against")
	}
}

// expiredServer is a TLS server whose certificate, signed by a CA the test trusts, ended an hour ago.
func expiredServer(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-47 * time.Hour), NotAfter: time.Now().Add(-time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	file := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: pemCertBlock, Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, file
}

func TestAnExpiredServerCertificateFailsAndTheTokenIsNotSent(t *testing.T) {
	reached := false
	srv, caFile := expiredServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	tr, err := newCATransport(caFile)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	if resp, err := (&http.Client{Transport: tr}).Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a certificate that has expired was accepted")
	}
	if reached {
		t.Fatal("the request, with its token, reached a server whose certificate had expired")
	}
}
