package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTLSSettingsFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
		tls     bool
		plain   bool
		port    string
	}{
		{"nothing set keeps the plain listener only", map[string]string{}, "", false, true, "8443"},
		{"certificate and key turn TLS on beside plain", map[string]string{envTLSCertFile: "c", envTLSKeyFile: "k"}, "", true, true, "8443"},
		{"plain off with TLS", map[string]string{envTLSCertFile: "c", envTLSKeyFile: "k", envPlainListener: plainOff, envTLSPort: "9443"}, "", true, false, "9443"},
		{"certificate without key", map[string]string{envTLSCertFile: "c"}, "set together", false, true, ""},
		{"key without certificate", map[string]string{envTLSKeyFile: "k"}, "set together", false, true, ""},
		{"plain off without TLS serves nothing", map[string]string{envPlainListener: plainOff}, "serve nothing", false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := tlsSettingsFromEnv(envOf(c.env))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil || got.enabled() != c.tls || got.plain != c.plain || got.port != c.port {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

// testAuthority is a CA that signs a serving certificate for 127.0.0.1.
type testAuthority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newAuthority(t *testing.T) *testAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testAuthority{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// writeLeaf signs a certificate for 127.0.0.1 and writes it with its key into dir.
func (a *testAuthority) writeLeaf(t *testing.T, dir string, serial int64) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "gateway"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	write(t, certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	write(t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certFile, keyFile
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func clientTrusting(pemBytes []byte) *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemBytes)
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
}

// start runs the servers and stops them when the test ends.
func start(t *testing.T, settings tlsSettings, plainPort string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	servers, err := buildServers(ctx, settings, plainPort, handler, func(err error) { t.Errorf("fatal: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range servers {
		go func() { _ = g.serve() }()
	}
	t.Cleanup(func() {
		cancel()
		for _, g := range servers {
			_ = g.srv.Close()
		}
	})
}

func get(c *http.Client, url string) (string, error) {
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestTheGatewayServesTLSToAClientThatTrustsItsCAAndRefusesOthers(t *testing.T) {
	ca := newAuthority(t)
	certFile, keyFile := ca.writeLeaf(t, t.TempDir(), 2)
	httpsPort, plainPort := freePort(t), freePort(t)
	start(t, tlsSettings{certFile: certFile, keyFile: keyFile, port: httpsPort, plain: true}, plainPort)
	url := "https://127.0.0.1:" + httpsPort + "/"

	eventually(t, "https answers a client that trusts the CA", func() bool {
		body, err := get(clientTrusting(ca.pem), url)
		return err == nil && body == "ok"
	})
	if _, err := get(clientTrusting(newAuthority(t).pem), url); err == nil {
		t.Fatal("a client that does not trust the CA was served")
	}
	if body, err := get(http.DefaultClient, "http://127.0.0.1:"+plainPort+"/"); err != nil || body != "ok" {
		t.Fatalf("plain listener: %q, %v", body, err)
	}
}

func TestWithThePlainListenerOffNothingAnswersInTheClear(t *testing.T) {
	ca := newAuthority(t)
	certFile, keyFile := ca.writeLeaf(t, t.TempDir(), 2)
	httpsPort, plainPort := freePort(t), freePort(t)
	start(t, tlsSettings{certFile: certFile, keyFile: keyFile, port: httpsPort, plain: false}, plainPort)
	eventually(t, "https answers", func() bool {
		body, err := get(clientTrusting(ca.pem), "https://127.0.0.1:"+httpsPort+"/")
		return err == nil && body == "ok"
	})
	if _, err := get(http.DefaultClient, "http://127.0.0.1:"+plainPort+"/"); err == nil {
		t.Fatal("the plain listener answered although it is off")
	}
}

func TestARenewedCertificateIsServedWithoutARestart(t *testing.T) {
	ca := newAuthority(t)
	dir := t.TempDir()
	certFile, keyFile := ca.writeLeaf(t, dir, 2)
	httpsPort := freePort(t)
	start(t, tlsSettings{certFile: certFile, keyFile: keyFile, port: httpsPort, plain: false}, "0")
	serial := func() int64 {
		conn, err := tls.Dial("tcp", "127.0.0.1:"+httpsPort, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // reads the served certificate's serial only
		if err != nil {
			return 0
		}
		defer func() { _ = conn.Close() }()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
	}
	eventually(t, "first certificate", func() bool { return serial() == 2 })
	ca.writeLeaf(t, dir, 3)
	eventually(t, "renewed certificate", func() bool { return serial() == 3 })
}

func TestBuildServersFailsOnAnUnreadableCertificate(t *testing.T) {
	_, err := buildServers(context.Background(), tlsSettings{certFile: "/nonexistent/c", keyFile: "/nonexistent/k", port: "0", plain: true}, "0", http.NotFoundHandler(), func(error) {})
	if err == nil {
		t.Fatal("an unreadable certificate was accepted")
	}
}
