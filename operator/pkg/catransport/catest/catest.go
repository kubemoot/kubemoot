// Package catest holds helpers that tests of TLS clients and servers share: a CA file that
// trusts a test server, a CA file that trusts nothing the test serves, and one request through a
// transport.
package catest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const pemCertificate = "CERTIFICATE"

// CertPEM is the PEM of a test server's own certificate, which stands as its CA.
func CertPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: srv.Certificate().Raw})
}

// WriteCA writes a CA file that trusts srv and returns its path.
func WriteCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(file, CertPEM(srv), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// UnrelatedCA writes a CA file holding a certificate that signed nothing the test serves.
func UnrelatedCA(t *testing.T) string {
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
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// Get sends one GET through tr and returns the status code.
func Get(t *testing.T, tr http.RoundTripper, url string) (int, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// Serving is a CA and a serving certificate it signed, as the files a service mounts.
type Serving struct {
	// CAFile verifies the certificate; CertFile and KeyFile are what the server serves.
	CAFile, CertFile, KeyFile string
}

// IssueServing makes a CA and a serving certificate for dnsNames and 127.0.0.1, valid for the
// given life from an hour ago, and writes the three files in a temporary directory.
func IssueServing(t *testing.T, life time.Duration, dnsNames ...string) Serving {
	t.Helper()
	caKey, caDER := newCA(t)
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "serving"},
		DNSNames: dnsNames, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(life - time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, tpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := Serving{
		CAFile: filepath.Join(dir, "ca.crt"), CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key"),
	}
	for file, data := range map[string][]byte{
		out.CAFile:   pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: caDER}),
		out.CertFile: pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: leafDER}),
		out.KeyFile:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	} {
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func newCA(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "test-install-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, der
}
