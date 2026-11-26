package main

import (
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/kubemoot/kubemoot/operator/pkg/filereload"
)

// The environment of the account broker's TLS listener: the serving certificate and key the
// operator issued from the install CA and the chart mounts, and the opt-in to plain http.
// envTrue is the value of a setting that is on.
const envTrue = "true"

const (
	envBrokerCertFile      = "LIAISON_BROKER_TLS_CERT_FILE"
	envBrokerKeyFile       = "LIAISON_BROKER_TLS_KEY_FILE"
	envAllowInsecureBroker = "LIAISON_ALLOW_INSECURE_BROKER"
)

// errBrokerNeedsTLS is the refusal to serve the broker with no certificate and no opt-in. The
// broker carries a person's account token and a gateway's ServiceAccount token.
var errBrokerNeedsTLS = errors.New("the account broker carries bearer tokens and has no serving certificate (" +
	envBrokerCertFile + " and " + envBrokerKeyFile + "): it will not serve plain http unless " +
	envAllowInsecureBroker + "=true")

// brokerTLSConfig returns the TLS settings of the broker's listener, or nil when the broker
// serves plain http because LIAISON_ALLOW_INSECURE_BROKER=true says so and no certificate is set.
// With a certificate the listener is TLS only: a plain http request is refused by the handshake.
func brokerTLSConfig(getenv func(string) string) (*tls.Config, error) {
	certFile, keyFile := getenv(envBrokerCertFile), getenv(envBrokerKeyFile)
	if certFile == "" && keyFile == "" {
		if getenv(envAllowInsecureBroker) == envTrue {
			return nil, nil
		}
		return nil, errBrokerNeedsTLS
	}
	if certFile == "" || keyFile == "" {
		return nil, errors.New(envBrokerCertFile + " and " + envBrokerKeyFile + " are set together")
	}
	// The certificate and key are read again when either file changes, so a renewal is served
	// without a restart; an unreadable or mismatched pair fails the handshake.
	keys := filereload.New(func(contents [][]byte) (*tls.Certificate, error) {
		pair, err := tls.X509KeyPair(contents[0], contents[1])
		if err != nil {
			return nil, fmt.Errorf("the broker's certificate and key do not load: %w", err)
		}
		return &pair, nil
	}, certFile, keyFile)
	if _, err := keys.Get(); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return keys.Get() },
	}, nil
}
