// Package catransport is the one HTTP transport that verifies a service against the install CA's
// public certificates. The crew liaison uses it for the gateways and the report service, and a
// crew's gateway uses it for the account broker.
package catransport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/kubemoot/kubemoot/operator/pkg/filereload"
)

// Transport sends requests over TLS verified against the certificates in a file, and reads the
// file again when it changes, so a CA bundle the operator republishes (a rotation adds the new
// CA beside the old) is trusted without a restart. Only those certificates are trusted: the
// services it talks to are signed by the install CA and by nothing public. It is the one
// transport for calls that carry a token to a service the install CA signed.
type Transport struct {
	current *filereload.Loader[*http.Transport]
}

// New reads the CA file. An unreadable file or one with no certificate is an error.
func New(file string) (*Transport, error) {
	t := &Transport{current: filereload.New(func() (*http.Transport, error) { return build(file) }, file).
		OnReplace(func(old *http.Transport) { old.CloseIdleConnections() })}
	if _, err := t.get(); err != nil {
		return nil, err
	}
	return t, nil
}

// build makes the transport that trusts the certificates in file.
func build(file string) (*http.Transport, error) {
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("the CA file is not readable: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("the CA file holds no certificate")
	}
	return &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}, nil
}

// get returns the transport for the CA file as it is now.
func (t *Transport) get() (*http.Transport, error) {
	tr, err := t.current.Get()
	if err != nil {
		return nil, fmt.Errorf("the CA file: %w", err)
	}
	return tr, nil
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr, err := t.get()
	if err != nil {
		return nil, err
	}
	return tr.RoundTrip(req)
}
