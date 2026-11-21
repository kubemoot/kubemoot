// Package natstest starts an in-process NATS server with TLS and authentication, and writes
// the CA, certificates and credentials a client needs into a temporary directory. Tests in
// every Kubemoot Go module use it to show a client connects with the options and is refused
// without them.
package natstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
)

// Auth is the kind of credential the server demands.
type Auth int

// The kinds of credential.
const (
	AuthNone Auth = iota
	AuthUser
	AuthToken
	AuthCreds
)

// Options chooses what the server demands.
type Options struct {
	// TLS makes the server speak TLS only (a tls:// URL).
	TLS bool
	// MutualTLS also requires a client certificate signed by the CA. It implies TLS.
	MutualTLS bool
	Auth      Auth
	// JetStream enables JetStream with a file store in the test directory.
	JetStream bool
	// JetStreamKey and JetStreamCipher encrypt the file store at rest when set.
	JetStreamKey    string
	JetStreamCipher server.StoreCipher
}

// Env is a running server and everything a client needs.
type Env struct {
	Server *server.Server
	// URL is tls://host:port when the server demands TLS, nats://host:port otherwise.
	URL string
	// StoreDir is the JetStream store directory.
	StoreDir string
	// Config holds the matching client settings: the CA, the client certificate when the
	// server demands one, and the credential.
	Config natsconn.Config
}

// Start runs a server for the test and stops it when the test ends.
func Start(t testing.TB, o Options) *Env {
	t.Helper()
	dir := t.TempDir()
	so := &server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true}
	cfg := natsconn.Config{}
	scheme := "nats://"
	if o.TLS || o.MutualTLS {
		scheme = "tls://"
		cfg = writeTLS(t, dir, so, o.MutualTLS)
	}
	applyAuth(t, dir, so, &cfg, o.Auth)
	storeDir := filepath.Join(dir, "js")
	if o.JetStream {
		so.JetStream, so.StoreDir = true, storeDir
		so.JetStreamKey, so.JetStreamCipher = o.JetStreamKey, o.JetStreamCipher
	}
	s, err := server.NewServer(so)
	if err != nil {
		t.Fatalf("natstest: new server: %v", err)
	}
	go s.Start()
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("natstest: server not ready")
	}
	cfg.URL = scheme + s.Addr().String()
	return &Env{Server: s, URL: cfg.URL, StoreDir: storeDir, Config: cfg}
}

func applyAuth(t testing.TB, dir string, so *server.Options, cfg *natsconn.Config, a Auth) {
	t.Helper()
	switch a {
	case AuthUser:
		so.Username, so.Password = "kubemoot", "s3cret-pass"
		cfg.User, cfg.Password = "kubemoot", "s3cret-pass"
	case AuthToken:
		so.Authorization = "s3cret-token"
		cfg.Token = "s3cret-token"
	case AuthCreds:
		cfg.CredsFile = writeOperatorAuth(t, dir, so)
	}
}

// writeOperatorAuth sets the server to trust one operator and account, and writes a user's
// creds file, the credential form NATS accounts use.
func writeOperatorAuth(t testing.TB, dir string, so *server.Options) string {
	t.Helper()
	must := func(err error) {
		if err != nil {
			t.Fatalf("natstest: %v", err)
		}
	}
	okp, err := nkeys.CreateOperator()
	must(err)
	opub, err := okp.PublicKey()
	must(err)
	oc := jwt.NewOperatorClaims(opub)
	_, err = oc.Encode(okp)
	must(err)
	akp, err := nkeys.CreateAccount()
	must(err)
	apub, err := akp.PublicKey()
	must(err)
	ajwt, err := jwt.NewAccountClaims(apub).Encode(okp)
	must(err)
	ukp, err := nkeys.CreateUser()
	must(err)
	upub, err := ukp.PublicKey()
	must(err)
	ujwt, err := jwt.NewUserClaims(upub).Encode(akp)
	must(err)
	seed, err := ukp.Seed()
	must(err)
	creds, err := jwt.FormatUserConfig(ujwt, seed)
	must(err)
	so.TrustedOperators = []*jwt.OperatorClaims{oc}
	resolver := &server.MemAccResolver{}
	must(resolver.Store(apub, ajwt))
	so.AccountResolver = resolver
	path := filepath.Join(dir, "user.creds")
	must(os.WriteFile(path, creds, 0o600))
	return path
}

// writeTLS makes a CA, a server certificate for 127.0.0.1 and, when mutual, a client
// certificate, and points the server at them. It returns the matching client settings.
func writeTLS(t testing.TB, dir string, so *server.Options, mutual bool) natsconn.Config {
	t.Helper()
	caKey, caCert, caPEM := newCA(t)
	srvCertPEM, srvKeyPEM := newLeaf(t, caKey, caCert, "nats-server", x509.ExtKeyUsageServerAuth)
	pair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatalf("natstest: server key pair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	so.TLSConfig = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	so.TLSTimeout = 5
	cfg := natsconn.Config{CAFile: write(t, dir, "ca.crt", caPEM)}
	if mutual {
		so.TLSConfig.ClientCAs = pool
		so.TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert
		so.TLSVerify = true
		certPEM, keyPEM := newLeaf(t, caKey, caCert, "nats-client", x509.ExtKeyUsageClientAuth)
		cfg.ClientCertFile = write(t, dir, "client.crt", certPEM)
		cfg.ClientKeyFile = write(t, dir, "client.key", keyPEM)
	}
	return cfg
}

func write(t testing.TB, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("natstest: write %s: %v", name, err)
	}
	return p
}

func newCA(t testing.TB) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("natstest: ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "natstest CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("natstest: ca cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("natstest: parse ca: %v", err)
	}
	return key, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newLeaf(t testing.TB, caKey *ecdsa.PrivateKey, ca *x509.Certificate, cn string,
	usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("natstest: leaf key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatalf("natstest: serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("natstest: leaf cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("natstest: leaf key der: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
