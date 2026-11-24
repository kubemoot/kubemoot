package liaison

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// caTransport sends requests over TLS verified against the certificates in a file, and reads the
// file again when it changes, so a CA bundle the operator republishes (a rotation adds the new
// CA beside the old) is trusted without a restart. Only those certificates are trusted: the
// services it talks to are signed by the install CA and by nothing public. It is the one TLS
// transport of the liaison, used for every call that carries a token.
type caTransport struct {
	file string

	mu      sync.Mutex
	modTime time.Time
	size    int64
	current *http.Transport
}

// newCATransport reads the CA file. An unreadable file or one with no certificate is an error.
func newCATransport(file string) (*caTransport, error) {
	t := &caTransport{file: file}
	if _, err := t.transport(); err != nil {
		return nil, err
	}
	return t, nil
}

// transport returns the transport for the CA file as it is now.
func (t *caTransport) transport() (*http.Transport, error) {
	info, err := os.Stat(t.file)
	if err != nil {
		return nil, fmt.Errorf("the CA file is not readable: %w", err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current != nil && info.ModTime().Equal(t.modTime) && info.Size() == t.size {
		return t.current, nil
	}
	pem, err := os.ReadFile(t.file)
	if err != nil {
		return nil, fmt.Errorf("the CA file is not readable: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("the CA file holds no certificate")
	}
	if t.current != nil {
		t.current.CloseIdleConnections()
	}
	t.current = &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}
	t.modTime, t.size = info.ModTime(), info.Size()
	return t.current, nil
}

// RoundTrip implements http.RoundTripper.
func (t *caTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr, err := t.transport()
	if err != nil {
		return nil, err
	}
	return tr.RoundTrip(req)
}
