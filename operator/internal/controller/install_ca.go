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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Calls inside the cluster that carry a bearer token use TLS, and the cluster is not assumed to
// encrypt pod traffic. The install has one CA, a Secret in the operator's namespace that the
// operator creates when it is absent and never overwrites. The operator signs the serving
// certificates of the services that carry tokens (the report service, each crew's discussion
// gateway) with it, and publishes the CA's public certificate in a ConfigMap that the dashboard
// and the crew liaison mount to verify what they connect to.
const (
	// caLifetime is how long a CA the operator creates lasts.
	caLifetime = 10 * 365 * 24 * time.Hour
	// caMinRemaining is the least life a CA may have left to sign with. A CA closer to its end
	// is refused with a message, so a certificate is never issued that outlives it.
	caMinRemaining = 30 * 24 * time.Hour
	// caPublishEvery is how often the published bundle is compared with the CA Secret.
	caPublishEvery = time.Minute

	// leafLifetime is the lifetime of a serving certificate; it is renewed once a third is left.
	leafLifetime = 365 * 24 * time.Hour
	// leafBackdate covers clock skew between the operator and a pod's node.
	leafBackdate = time.Hour

	// secretKeyCACert is the CA certificate's key in the CA Secret and the published ConfigMap.
	secretKeyCACert = "ca.crt"
	// pemCertificate is the PEM block type of a certificate.
	pemCertificate = "CERTIFICATE"

	// LabelInstallCALeaf marks a Secret that holds a certificate the install CA signed, so
	// the operator can tell when no certificate still depends on a previous CA.
	LabelInstallCALeaf = "kubemoot.ai/install-ca-leaf"
)

// Environment the chart sets on the operator.
const (
	envInstallCASecret    = "INSTALL_CA_SECRET"
	envInstallCAConfigMap = "INSTALL_CA_CONFIGMAP"
	envOperatorNamespace  = "OPERATOR_NAMESPACE"
)

var installCALog = logf.Log.WithName("install-ca")

// errCAExpiring is returned when the CA has too little life left to sign with.
var errCAExpiring = fmt.Errorf("the install CA has expired or is within %d days of expiring: replace the CA Secret "+
	"(delete it to have the operator make a new one) and the certificates are re-issued", int(caMinRemaining.Hours()/24))

// InstallCA is the install's CA and the ConfigMap its public certificates are published in.
type InstallCA struct {
	Client client.Client
	// Namespace is the operator's, where both live.
	Namespace  string
	SecretName string
	ConfigMap  string
	Now        func() time.Time
	// PublishEvery is how often Start compares the bundle with the Secret; zero is caPublishEvery.
	PublishEvery time.Duration
}

// InstallCAFromEnv returns the CA the environment describes, or nil when no Secret is named.
func InstallCAFromEnv(c client.Client, getenv func(string) string) *InstallCA {
	secret := getenv(envInstallCASecret)
	if secret == "" {
		return nil
	}
	ns := getenv(envOperatorNamespace)
	if ns == "" {
		ns = defaultOperatorNamespace
	}
	configMap := getenv(envInstallCAConfigMap)
	if configMap == "" {
		configMap = secret
	}
	return &InstallCA{Client: c, Namespace: ns, SecretName: secret, ConfigMap: configMap, Now: time.Now}
}

// authority is a CA that signs serving certificates.
type authority struct {
	cert *x509.Certificate
	key  crypto.Signer
	pem  []byte
}

// parseAuthority reads a CA from the PEM of its certificate and private key.
func parseAuthority(certPEM, keyPEM []byte) (*authority, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("the CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("the certificate in the CA Secret is not a CA")
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return nil, err
	}
	if !publicKeysEqual(cert.PublicKey, key.Public()) {
		return nil, errors.New("the CA key does not belong to the CA certificate")
	}
	return &authority{cert: cert, key: key, pem: certPEM}, nil
}

// parsePrivateKey reads an RSA (PKCS#1), EC or PKCS#8 private key in PEM.
func parsePrivateKey(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("the private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the private key is not an RSA, EC or PKCS#8 key")
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, errors.New("the private key cannot sign")
	}
	return signer, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := a.(equaler)
	return ok && e.Equal(b)
}

// usable refuses a CA that has expired or has too little life left.
func (a *authority) usable(now time.Time) error {
	if now.Add(caMinRemaining).After(a.cert.NotAfter) || now.Before(a.cert.NotBefore) {
		return errCAExpiring
	}
	return nil
}

// newKeyAndSerial makes a P-256 key and a random certificate serial number.
func newKeyAndSerial() (*ecdsa.PrivateKey, *big.Int, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return key, serial, err
}

// encodeKeyPEM is the PEM of an EC private key.
func encodeKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// newCASecretData makes a CA and returns the Secret data that holds it.
func newCASecretData(now time.Time) (map[string][]byte, error) {
	key, serial, err := newKeyAndSerial()
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "kubemoot-install-ca"},
		NotBefore: now.Add(-leafBackdate), NotAfter: now.Add(caLifetime),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyPEM, err := encodeKeyPEM(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der})
	return map[string][]byte{corev1.TLSCertKey: certPEM, secretKeyCACert: certPEM, corev1.TLSPrivateKeyKey: keyPEM}, nil
}

// issue signs a serving certificate for dnsNames and returns it and its key in PEM. It ends no
// later than the CA does.
func (a *authority) issue(dnsNames []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	if err := a.usable(now); err != nil {
		return nil, nil, err
	}
	key, serial, err := newKeyAndSerial()
	if err != nil {
		return nil, nil, err
	}
	notAfter := now.Add(leafLifetime)
	if notAfter.After(a.cert.NotAfter) {
		notAfter = a.cert.NotAfter
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: dnsNames[0]}, DNSNames: dnsNames,
		NotBefore: now.Add(-leafBackdate), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = encodeKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}), keyPEM, nil
}

// signed reports whether leaf was signed by this CA.
func (a *authority) signed(leaf *x509.Certificate) bool {
	return leaf.CheckSignatureFrom(a.cert) == nil
}

// fresh reports whether secret data holds a certificate this CA signed for exactly dnsNames,
// whose key matches it, with more than a third of its life left. Anything else is replaced.
func (a *authority) fresh(data map[string][]byte, dnsNames []string, now time.Time) bool {
	leaf, err := tlsKeyPair(data)
	if err != nil || !sameNames(leaf.DNSNames, dnsNames) || !a.signed(leaf) {
		return false
	}
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	return now.Add(life/3).Before(leaf.NotAfter) && now.After(leaf.NotBefore)
}

// tlsKeyPair parses the certificate in secret data and checks that the key belongs to it.
func tlsKeyPair(data map[string][]byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data[corev1.TLSCertKey])
	if block == nil {
		return nil, errors.New("no certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := parsePrivateKey(data[corev1.TLSPrivateKeyKey])
	if err != nil {
		return nil, err
	}
	if !publicKeysEqual(leaf.PublicKey, key.Public()) {
		return nil, errors.New("the key does not belong to the certificate")
	}
	return leaf, nil
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, n := range a {
		seen[n] = true
	}
	for _, n := range b {
		if !seen[n] {
			return false
		}
	}
	return true
}

// serviceDNSNames are the names clients reach a Service by.
func serviceDNSNames(service, namespace string) []string {
	return []string{
		service,
		fmt.Sprintf("%s.%s", service, namespace),
		fmt.Sprintf("%s.%s.svc", service, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", service, namespace),
	}
}

// ServingCertificate returns the certificate source of a TLS listener for dnsNames: it issues
// one from the install CA on the first handshake, keeps it in memory (no Secret, no key on
// disk), and issues another when it is no longer fresh, which includes a new CA. When the CA
// cannot be read or signs nothing, the certificate it has is served while it is valid, so a short
// API server outage does not stop the service.
func (c *InstallCA) ServingCertificate(dnsNames []string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s := &servingCert{ca: c, names: dnsNames}
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.get() }
}

// servingCert is the certificate a listener serves and the data it was made from.
type servingCert struct {
	ca    *InstallCA
	names []string

	mu      sync.Mutex
	current *tls.Certificate
	data    map[string][]byte
	// checked is when current was last compared with the CA; within servingRecheck it is served
	// without reading the CA again, so a handshake does not wait on the API server.
	checked time.Time
}

const (
	// servingRecheck is how long a certificate is served before the CA is read again to see
	// whether it is still fresh (a new CA, a renewal due).
	servingRecheck = 30 * time.Second
	// servingCAReadTimeout bounds reading the CA for a handshake.
	servingCAReadTimeout = 5 * time.Second
)

func (s *servingCert) get() (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.ca.Now()
	if s.current != nil && now.Sub(s.checked) < servingRecheck && now.Before(s.current.Leaf.NotAfter) {
		return s.current, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), servingCAReadTimeout)
	defer cancel()
	ca, err := s.ca.Authority(ctx)
	if ca == nil || (err != nil && !errors.Is(err, errCAExpiring)) {
		return s.fallback(err)
	}
	if s.current != nil && ca.fresh(s.data, s.names, now) {
		s.checked = now
		return s.current, nil
	}
	cert, err := s.issue(ca, now)
	if err != nil {
		return s.fallback(err)
	}
	return cert, nil
}

// issue makes a new certificate from ca and holds it.
func (s *servingCert) issue(ca *authority, now time.Time) (*tls.Certificate, error) {
	crt, key, err := ca.issue(s.names, now)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(crt, key)
	if err != nil {
		return nil, err
	}
	if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
		return nil, err
	}
	s.current, s.data, s.checked = &pair, map[string][]byte{corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key}, now
	return s.current, nil
}

// fallback serves the certificate already held while it is valid, else returns err.
func (s *servingCert) fallback(err error) (*tls.Certificate, error) {
	if s.current != nil && s.ca.Now().Before(s.current.Leaf.NotAfter) {
		// Not read again for servingRecheck, so an outage does not make every handshake wait.
		s.checked = s.ca.Now()
		return s.current, nil
	}
	if err == nil {
		err = errors.New("no serving certificate is available")
	}
	return nil, err
}

// Authority reads the CA from its Secret, and makes the Secret first when there is none. It
// never replaces a Secret that exists, so a CA is never rotated by accident. A CA that has
// expired or is about to is refused (errCAExpiring).
func (c *InstallCA) Authority(ctx context.Context) (*authority, error) {
	key := types.NamespacedName{Namespace: c.Namespace, Name: c.SecretName}
	secret := &corev1.Secret{}
	err := c.Client.Get(ctx, key, secret)
	// The read may come from the manager's cache, which can lag a Secret another replica or this
	// one has just made; the next attempt (the publisher tries every minute) finds it.
	if apierrors.IsNotFound(err) {
		secret, err = c.create(ctx, key)
	}
	if err != nil {
		return nil, fmt.Errorf("the install CA Secret %s/%s: %w", key.Namespace, key.Name, err)
	}
	a, err := parseAuthority(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
	if err != nil {
		return nil, fmt.Errorf("the install CA Secret %s/%s: %w", key.Namespace, key.Name, err)
	}
	return a, a.usable(c.Now())
}

// create makes the CA Secret. Another replica may win the race; its Secret is read instead.
func (c *InstallCA) create(ctx context.Context, key types.NamespacedName) (*corev1.Secret, error) {
	data, err := newCASecretData(c.Now())
	if err != nil {
		return nil, err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": managedByValue}},
		Type: corev1.SecretTypeTLS, Data: data,
	}
	if err := c.Client.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		secret = &corev1.Secret{}
		return secret, c.Client.Get(ctx, key, secret)
	}
	installCALog.Info("Made the install CA", "secret", key.String())
	return secret, nil
}

// PublishCA writes the CA bundle into the ConfigMap clients mount: the current CA, and the
// previous ones until no certificate depends on them. Clients therefore trust both through a
// rotation, and a certificate signed by the old CA verifies until it is replaced. The bundle
// never holds a key. The ConfigMap is written only when the bundle changes.
func (c *InstallCA) PublishCA(ctx context.Context) error {
	ca, err := c.Authority(ctx)
	if err != nil && ca == nil {
		return err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: c.ConfigMap, Namespace: c.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, c.Client, cm, func() error {
		bundle, berr := c.bundle(ctx, ca, cm.Data[secretKeyCACert])
		if berr != nil {
			return berr
		}
		cm.Data = map[string]string{secretKeyCACert: bundle}
		return nil
	})
	return err
}

// bundle is the PEM of the current CA followed by the previous certificates still in use. A
// previous CA is in use while a labelled leaf is signed by it; the CAs come from the published
// bundle and from the ca.crt each leaf carries, so the bundle is rebuilt if the ConfigMap is lost.
func (c *InstallCA) bundle(ctx context.Context, current *authority, published string) (string, error) {
	out := string(current.pem)
	staleCAs, err := c.staleLeafCAs(ctx, current)
	if err != nil {
		return "", err
	}
	candidates := append(certificatesIn([]byte(published)), staleCAs.issuers...)
	if !staleCAs.inUse {
		return out, nil
	}
	seen := map[string]bool{string(current.cert.Raw): true}
	for _, old := range candidates {
		if seen[string(old.Raw)] || !old.NotAfter.After(c.Now()) {
			continue
		}
		seen[string(old.Raw)] = true
		out += string(pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: old.Raw}))
	}
	return out, nil
}

// certificatesIn parses every certificate in a PEM bundle, skipping what is not one.
func certificatesIn(bundle []byte) []*x509.Certificate {
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, bundle = pem.Decode(bundle)
		if block == nil {
			return certs
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			certs = append(certs, cert)
		}
	}
}

// staleLeaves is what the labelled leaves say about the CAs that signed them.
type staleLeaves struct {
	// inUse is true when some usable leaf is signed by something other than the current CA.
	inUse bool
	// issuers are the CA certificates those leaves carry in their ca.crt.
	issuers []*x509.Certificate
}

// staleLeafCAs reads every Secret labelled LabelInstallCALeaf. A leaf that cannot be used (its
// certificate or key does not parse or match) is skipped and logged: no client can use it, so it
// does not hold a previous CA in the bundle.
func (c *InstallCA) staleLeafCAs(ctx context.Context, current *authority) (staleLeaves, error) {
	var out staleLeaves
	secrets := &corev1.SecretList{}
	if err := c.Client.List(ctx, secrets, client.HasLabels{LabelInstallCALeaf}); err != nil {
		return out, err
	}
	for i := range secrets.Items {
		leaf, err := tlsKeyPair(secrets.Items[i].Data)
		if err != nil {
			installCALog.Info("Skipping an unusable certificate Secret", "secret",
				client.ObjectKeyFromObject(&secrets.Items[i]).String(), "reason", err.Error())
			continue
		}
		if current.signed(leaf) {
			continue
		}
		out.inUse = true
		out.issuers = append(out.issuers, certificatesIn(secrets.Items[i].Data[secretKeyCACert])...)
	}
	return out, nil
}

// EnsureLeaf makes sure the Secret at key holds a current serving certificate for dnsNames signed by
// the install CA, issuing one when it is absent or no longer fresh. The Secret is labelled
// LabelInstallCALeaf and carries the signing CA in ca.crt; own, when set, makes the Secret owned (for
// garbage collection). It returns when the certificate ends.
func (c *InstallCA) EnsureLeaf(
	ctx context.Context, key client.ObjectKey, dnsNames []string, own func(*corev1.Secret) error,
) (time.Time, error) {
	ca, err := c.Authority(ctx)
	if err != nil {
		return time.Time{}, err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	err = c.Client.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return time.Time{}, err
	}
	if err == nil && ca.fresh(secret.Data, dnsNames, c.Now()) {
		return notAfterOf(secret.Data), nil
	}
	certPEM, keyPEM, err := ca.issue(dnsNames, c.Now())
	if err != nil {
		return time.Time{}, err
	}
	_, err = controllerutil.CreateOrUpdate(ctx, c.Client, secret, func() error {
		secret.Type = corev1.SecretTypeTLS
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[LabelInstallCALeaf] = valueTrue
		secret.Data = map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM, secretKeyCACert: ca.pem}
		if own != nil {
			return own(secret)
		}
		return nil
	})
	return notAfterOf(secret.Data), err
}

// notAfterOf is when the certificate in secret data ends; the zero time when it cannot be read.
func notAfterOf(data map[string][]byte) time.Time {
	leaf, err := tlsKeyPair(data)
	if err != nil {
		return time.Time{}
	}
	return leaf.NotAfter
}

// NeedLeaderElection is false: every replica publishes the same bundle, and an operator that is
// not the leader must still let the dashboard start.
func (c *InstallCA) NeedLeaderElection() bool { return false }

// Start implements manager.Runnable: it makes the CA when there is none, publishes the bundle,
// and compares it with the CA Secret every minute so a replaced CA reaches clients without a restart.
func (c *InstallCA) Start(ctx context.Context) error {
	return runEvery(ctx, c.PublishEvery, func(ctx context.Context) {
		if err := c.PublishCA(ctx); err != nil {
			installCALog.Info("The install CA bundle is not published", "reason", err.Error())
		}
	})
}

// runEvery calls fn at once and then every interval (caPublishEvery when interval is not set)
// until ctx ends. A Runnable that keeps a Secret or ConfigMap current uses it.
func runEvery(ctx context.Context, interval time.Duration, fn func(context.Context)) error {
	if interval <= 0 {
		interval = caPublishEvery
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		fn(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// envReportService is the name of the operator's report Service, which the chart sets.
const envReportService = "REPORT_SERVICE_NAME"

// EnvReportAllowInsecure lets the report service run without TLS (a development run).
const EnvReportAllowInsecure = "REPORT_ALLOW_INSECURE"

// ReportCertificate is the serving certificate source of the report service, issued from the
// install CA for the Service's names, or nil when there is no CA or no Service name (a run
// outside the chart), in which case the service refuses to start unless it is told to accept plain http.
func ReportCertificate(ca *InstallCA, getenv func(string) string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	service := getenv(envReportService)
	if ca == nil || service == "" {
		return nil
	}
	return ca.ServingCertificate(serviceDNSNames(service, ca.Namespace))
}
