package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
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
	keys := &reloadingKeyPair{certFile: certFile, keyFile: keyFile}
	if _, err := keys.get(); err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: keys.getCertificate}, nil
}

// reloadingKeyPair serves the certificate and key in two files and reads them again when the
// certificate file changes, so a certificate the operator renews is served without a restart.
type reloadingKeyPair struct {
	certFile, keyFile string

	mu      sync.Mutex
	modTime time.Time
	size    int64
	current *tls.Certificate
}

func (k *reloadingKeyPair) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return k.get()
}

// get returns the pair as the files hold it now. An unreadable or mismatched pair is an error,
// never a stale certificate: the handshake fails and the caller retries.
func (k *reloadingKeyPair) get() (*tls.Certificate, error) {
	info, err := os.Stat(k.certFile)
	if err != nil {
		return nil, fmt.Errorf("the broker's certificate is not readable: %w", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.current != nil && info.ModTime().Equal(k.modTime) && info.Size() == k.size {
		return k.current, nil
	}
	pair, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		return nil, fmt.Errorf("the broker's certificate and key do not load: %w", err)
	}
	k.current, k.modTime, k.size = &pair, info.ModTime(), info.Size()
	return k.current, nil
}
