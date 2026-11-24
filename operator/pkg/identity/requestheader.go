/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The ConfigMap the API server publishes for the servers it aggregates, and the keys of it
// that say how it forwards the caller it authenticated.
const (
	FrontProxyConfigNamespace = "kube-system"
	FrontProxyConfigName      = "extension-apiserver-authentication"

	keyRequestHeaderCA        = "requestheader-client-ca-file"
	keyRequestHeaderAllowed   = "requestheader-allowed-names"
	keyRequestHeaderUsernames = "requestheader-username-headers"
	keyRequestHeaderGroups    = "requestheader-group-headers"
	keyRequestHeaderExtra     = "requestheader-extra-headers-prefix"
)

// ErrFrontProxyNotLoaded means the front proxy's CA has not been read yet, so no request can
// be attributed to a caller. A door answers 503: the check cannot be made.
var ErrFrontProxyNotLoaded = errors.New("the API server's front proxy configuration is not loaded")

// FrontProxyConfig is how the API server forwards a caller it authenticated to an aggregated
// server: the CA of the client certificate it presents, the certificate names it may use, and
// the headers that carry the user, the groups and the extra values.
type FrontProxyConfig struct {
	// ClientCAs verifies the certificate the API server presents.
	ClientCAs *x509.CertPool
	// AllowedNames are the certificate common names the API server may present. Empty
	// means any certificate that chains to ClientCAs, as in the API server's own rule.
	AllowedNames    []string
	UsernameHeaders []string
	GroupHeaders    []string
	ExtraPrefixes   []string
}

// ParseFrontProxyConfig reads the extension-apiserver-authentication ConfigMap data. A
// ConfigMap with no usable CA, or with no username header, is refused: with either missing
// no forwarded identity could be trusted or read.
func ParseFrontProxyConfig(data map[string]string) (*FrontProxyConfig, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(data[keyRequestHeaderCA])) {
		return nil, fmt.Errorf("%s holds no certificate", keyRequestHeaderCA)
	}
	cfg := &FrontProxyConfig{ClientCAs: pool}
	for key, dst := range map[string]*[]string{
		keyRequestHeaderAllowed: &cfg.AllowedNames, keyRequestHeaderUsernames: &cfg.UsernameHeaders,
		keyRequestHeaderGroups: &cfg.GroupHeaders, keyRequestHeaderExtra: &cfg.ExtraPrefixes,
	} {
		list, err := stringList(data[key])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		*dst = list
	}
	if len(cfg.UsernameHeaders) == 0 {
		return nil, fmt.Errorf("%s names no header", keyRequestHeaderUsernames)
	}
	return cfg, nil
}

// stringList reads a JSON list of strings, or an empty list from an empty value.
func stringList(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RequestHeaderTrust holds the front proxy configuration the API server publishes, read from
// the cluster and kept fresh so a rotated CA is trusted without a restart. Until a read
// succeeds nothing is trusted. A later failed read keeps the last good configuration: the CA
// the API server presents changes rarely and never silently, so refusing everyone because the
// ConfigMap could not be read for a moment would turn a hiccup into an outage.
type RequestHeaderTrust struct {
	current atomic.Pointer[FrontProxyConfig]
}

// NewRequestHeaderTrust returns a trust that holds cfg; a nil cfg trusts nothing until Load.
func NewRequestHeaderTrust(cfg *FrontProxyConfig) *RequestHeaderTrust {
	t := &RequestHeaderTrust{}
	t.current.Store(cfg)
	return t
}

// Config is the current front proxy configuration, or nil before the first successful read.
func (t *RequestHeaderTrust) Config() *FrontProxyConfig { return t.current.Load() }

// Load reads the ConfigMap once and replaces the configuration.
func (t *RequestHeaderTrust) Load(ctx context.Context, c kubernetes.Interface) error {
	cm, err := c.CoreV1().ConfigMaps(FrontProxyConfigNamespace).Get(ctx, FrontProxyConfigName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read %s/%s: %w", FrontProxyConfigNamespace, FrontProxyConfigName, err)
	}
	cfg, err := ParseFrontProxyConfig(cm.Data)
	if err != nil {
		return fmt.Errorf("%s/%s: %w", FrontProxyConfigNamespace, FrontProxyConfigName, err)
	}
	t.current.Store(cfg)
	return nil
}

// Refresh reads the ConfigMap at the start and then every interval until ctx ends, and
// reports each failed read to onError. It returns when ctx is done.
func (t *RequestHeaderTrust) Refresh(
	ctx context.Context, c kubernetes.Interface, interval time.Duration, onError func(error),
) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := t.Load(ctx, c); err != nil && ctx.Err() == nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// ServerTLSConfig is the TLS configuration of a server only the API server may call: it asks
// for a client certificate and verifies it against the CA current at each handshake, so a
// rotated CA applies to new connections. With no CA loaded the handshake fails.
func (t *RequestHeaderTrust) ServerTLSConfig(
	getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), http2 bool,
) *tls.Config {
	base := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: getCertificate, NextProtos: []string{"http/1.1"}}
	if http2 {
		base.NextProtos = []string{"h2", "http/1.1"}
	}
	base.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		cfg := t.Config()
		if cfg == nil {
			return nil, ErrFrontProxyNotLoaded
		}
		c := base.Clone()
		c.GetConfigForClient = nil
		c.ClientAuth = tls.RequireAndVerifyClientCert
		c.ClientCAs = cfg.ClientCAs
		return c, nil
	}
	return base
}

// RequestHeaderVerifier reads the caller the API server authenticated and forwarded in
// request headers. The headers are trusted only when the request arrived over TLS with a
// client certificate that verifies against the front proxy CA and whose common name is
// allowed; any other request, with or without such headers, has no credential of this kind.
// The check is made here, on the request, and does not rely on the listener having made it.
type RequestHeaderVerifier struct {
	Trust *RequestHeaderTrust
}

// Verify implements Verifier. A request that did not come through the API server is
// ErrNoCredential, so a forged header is never read; a request from the API server that names
// no user is ErrInvalid.
func (v RequestHeaderVerifier) Verify(r *http.Request) (*Identity, error) {
	cfg := v.Trust.Config()
	if cfg == nil {
		return nil, ErrFrontProxyNotLoaded
	}
	if !fromFrontProxy(r, cfg) {
		return nil, ErrNoCredential
	}
	user := firstHeader(r.Header, cfg.UsernameHeaders)
	if user == "" {
		return nil, ErrInvalid
	}
	return &Identity{
		Issuer: IssuerKubernetes, Subject: user,
		Groups: headerValues(r.Header, cfg.GroupHeaders), Extra: extraValues(r.Header, cfg.ExtraPrefixes),
	}, nil
}

// FromFrontProxy reports whether the request came from the API server: its TLS client
// certificate verifies against the front proxy CA and carries an allowed name. It reads no
// header, so it serves requests the API server makes for itself, such as discovery.
func (v RequestHeaderVerifier) FromFrontProxy(r *http.Request) (bool, error) {
	cfg := v.Trust.Config()
	if cfg == nil {
		return false, ErrFrontProxyNotLoaded
	}
	return fromFrontProxy(r, cfg), nil
}

// fromFrontProxy reports whether the request's TLS client certificate verifies against the
// front proxy CA for client use and carries an allowed common name.
func fromFrontProxy(r *http.Request, cfg *FrontProxyConfig) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	leaf := r.TLS.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range r.TLS.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots: cfg.ClientCAs, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return false
	}
	return len(cfg.AllowedNames) == 0 || slices.Contains(cfg.AllowedNames, leaf.Subject.CommonName)
}

func firstHeader(h http.Header, names []string) string {
	for _, n := range names {
		if v := strings.TrimSpace(h.Get(n)); v != "" {
			return v
		}
	}
	return ""
}

func headerValues(h http.Header, names []string) []string {
	var out []string
	for _, n := range names {
		for _, v := range h.Values(n) {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// extraValues reads the headers that start with a prefix: the rest of the name, lower-cased
// and percent-decoded, is the key, as the API server writes them.
func extraValues(h http.Header, prefixes []string) map[string][]string {
	out := map[string][]string{}
	for name, values := range h {
		for _, p := range prefixes {
			if len(name) <= len(p) || !strings.EqualFold(name[:len(p)], p) {
				continue
			}
			key, err := url.PathUnescape(name[len(p):])
			if err != nil {
				continue
			}
			key = strings.ToLower(key)
			out[key] = append(out[key], values...)
		}
	}
	return out
}
