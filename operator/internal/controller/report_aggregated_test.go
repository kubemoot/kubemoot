/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/identitytest"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	proxyName      = "front-proxy-client"
	forwardedUser  = "jane"
	forgedUser     = "system:admin"
	memoryExport   = aggregatedBase + "/namespaces/tenant/crews/pgm/memory-export"
	componentsPath = aggregatedBase + "/componentstatuses"
)

// aggregatedFixture is the aggregated listener over a fake cluster whose RBAC allows or not.
type aggregatedFixture struct {
	server  *AggregatedReportServer
	ca      *identitytest.CA
	cluster *fake.Clientset
	asked   *[]authorizationv1.SubjectAccessReview
}

func newAggregatedFixture(t *testing.T, allowed bool) *aggregatedFixture {
	t.Helper()
	cluster := fake.NewSimpleClientset()
	var asked []authorizationv1.SubjectAccessReview
	cluster.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		asked = append(asked, *a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview))
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	ca := identitytest.NewCA(t, "front-proxy-ca")
	cfg, err := identity.ParseFrontProxyConfig(map[string]string{
		"requestheader-client-ca-file":       string(ca.PEM),
		"requestheader-allowed-names":        `["` + proxyName + `"]`,
		"requestheader-username-headers":     `["X-Remote-User"]`,
		"requestheader-group-headers":        `["X-Remote-Group"]`,
		"requestheader-extra-headers-prefix": `["X-Remote-Extra-"]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	reports := &ReportServer{
		Client: ctrlfake.NewClientBuilder().WithScheme(scheme).Build(),
		Memory: fakeOpener{reader: crewFacts()},
		Auth:   NewReviewAuthorizer(cluster),
	}
	return &aggregatedFixture{
		server:  &AggregatedReportServer{Reports: reports, Trust: identity.NewRequestHeaderTrust(cfg)},
		ca:      ca,
		cluster: cluster,
		asked:   &asked,
	}
}

// proxyRequest is a request as the API server forwards it for a user, with the TLS state of
// a verified connection when cert is set.
func proxyRequest(path string, cert *tls.Certificate) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Remote-User", forwardedUser)
	r.Header.Add("X-Remote-Group", "devs")
	r.Header.Set("X-Remote-Extra-Scopes", "a")
	if cert != nil {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert.Leaf}}
	}
	return r
}

func (f *aggregatedFixture) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, r)
	return rec
}

func (f *aggregatedFixture) proxyCert(t *testing.T) *tls.Certificate {
	t.Helper()
	c := f.ca.Issue(t, proxyName, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	return &c
}

func TestAggregatedRouteAsksTheForwardedCallersAccess(t *testing.T) {
	f := newAggregatedFixture(t, true)
	rec := f.serve(proxyRequest(memoryExport, f.proxyCert(t)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(*f.asked) != 1 {
		t.Fatalf("access reviews = %d, want 1", len(*f.asked))
	}
	got := (*f.asked)[0].Spec
	want := crewAccess(mustScope(t, tenantNS, "pgm"))
	if got.User != forwardedUser || len(got.Groups) != 1 || got.Groups[0] != "devs" ||
		got.Extra["scopes"][0] != "a" || *got.ResourceAttributes != want {
		t.Errorf("review = %+v / %+v, want the forwarded caller asking %+v", got, got.ResourceAttributes, want)
	}
}

func TestAggregatedRouteRefusesAForwardedCallerWithoutRBAC(t *testing.T) {
	f := newAggregatedFixture(t, false)
	rec := f.serve(proxyRequest(memoryExport, f.proxyCert(t)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "PK") {
		t.Error("a refused caller must receive no export")
	}
}

func TestAggregatedRouteIgnoresForgedHeadersWithoutAVerifiedProxyCertificate(t *testing.T) {
	f := newAggregatedFixture(t, true)
	rogue := identitytest.NewCA(t, "rogue-ca").Issue(t, proxyName, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	other := f.ca.Issue(t, "not-the-proxy", x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	cases := map[string]*http.Request{
		"plain HTTP":                  proxyRequest(memoryExport, nil),
		"another CA's certificate":    proxyRequest(memoryExport, &rogue),
		"a name that is not allowed":  proxyRequest(memoryExport, &other),
		"a bearer token and no proxy": withBearer(proxyRequest(memoryExport, nil)),
	}
	for name, r := range cases {
		r.Header.Set("X-Remote-User", forgedUser)
		r.Header.Set("X-Remote-Group", "system:masters")
		if rec := f.serve(r); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}
	if len(*f.asked) != 0 {
		t.Errorf("a forged caller reached the access review %d times; the forged subject must never be asked about", len(*f.asked))
	}
	for _, a := range f.cluster.Actions() {
		if a.GetResource().Resource == "tokenreviews" {
			t.Error("the aggregated listener must not review bearer tokens")
		}
	}
}

func mustScope(t *testing.T, namespace, crew string) crewscope.Scope {
	t.Helper()
	scope, err := crewscope.New(namespace, crew)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func withBearer(r *http.Request) *http.Request {
	r.Header.Set("Authorization", "Bearer "+sampleToken)
	return r
}

func TestAggregatedRouteAnswers503BeforeTheFrontProxyConfigurationIsLoaded(t *testing.T) {
	f := newAggregatedFixture(t, true)
	cert := f.proxyCert(t)
	f.server.Trust = identity.NewRequestHeaderTrust(nil)
	if rec := f.serve(proxyRequest(memoryExport, cert)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestAggregatedRouteFailsClosedWithoutAnAuthorizer(t *testing.T) {
	f := newAggregatedFixture(t, true)
	f.server.Reports.Auth = nil
	if rec := f.serve(proxyRequest(memoryExport, f.proxyCert(t))); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestEveryReportRouteIsServedOnTheAggregatedAPIBehindTheGate(t *testing.T) {
	f := newAggregatedFixture(t, false)
	table := f.server.Reports.routeTable()
	if len(aggregatedPaths) != len(table) {
		t.Errorf("aggregated paths = %d, routes = %d: every route needs exactly one aggregated path", len(aggregatedPaths), len(table))
	}
	cert := f.proxyCert(t)
	for _, rt := range table {
		path, ok := aggregatedPaths[rt.pattern]
		if !ok {
			t.Errorf("%s has no aggregated path", rt.pattern)
			continue
		}
		url := wildcard.ReplaceAllString(aggregatedBase+path, "x")
		if rec := f.serve(proxyRequest(url, nil)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without the front proxy: status = %d, want 401", url, rec.Code)
		}
		if rec := f.serve(proxyRequest(url, cert)); rec.Code != http.StatusForbidden {
			t.Errorf("%s for a caller without RBAC: status = %d, want 403", url, rec.Code)
		}
	}
}

func TestAggregatedRoutesReachEachHandlerForACallerWithRBAC(t *testing.T) {
	f := newAggregatedFixture(t, true)
	cert := f.proxyCert(t)
	want := map[string]int{
		componentsPath: http.StatusOK,
		aggregatedBase + "/namespaces/tenant/crews/pgm/memory-export":                      http.StatusOK,
		aggregatedBase + "/namespaces/tenant/crews/pgm/conversation-export":                http.StatusBadRequest, // no id
		aggregatedBase + "/namespaces/tenant/crewfitnesssuites/nightly/report":             http.StatusInternalServerError,
		aggregatedBase + "/namespaces/tenant/crews/pgm/memory-export?allowUnscreened=true": http.StatusBadRequest,
	}
	for path, code := range want {
		if rec := f.serve(proxyRequest(path, cert)); rec.Code != code {
			t.Errorf("%s: status = %d, want %d: %s", path, rec.Code, code, rec.Body)
		}
	}
}

func TestAggregatedAPIServesNoPlainRoute(t *testing.T) {
	f := newAggregatedFixture(t, true)
	for _, path := range []string{"/memory-export/tenant/pgm", strings.TrimPrefix(componentsPath, aggregatedBase), "/report/tenant/x", "/healthz"} {
		if rec := f.serve(proxyRequest(path, f.proxyCert(t))); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestAggregatedDiscoveryListsTheReportsForTheAPIServer(t *testing.T) {
	f := newAggregatedFixture(t, false) // discovery needs no RBAC: it carries no report data
	rec := f.serve(proxyRequest(aggregatedBase, f.proxyCert(t)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list metav1.APIResourceList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range list.APIResources {
		names[r.Name] = true
	}
	for _, want := range []string{"crews/memory-export", "crews/conversation-export", "crewfitnesssuites/report", "componentstatuses"} {
		if !names[want] {
			t.Errorf("discovery lacks %s: %v", want, names)
		}
	}
	if list.Kind != "APIResourceList" || list.GroupVersion != "reports.kubemoot.ai/v1alpha1" {
		t.Errorf("discovery = %s %s", list.Kind, list.GroupVersion)
	}
}

func TestAggregatedDiscoveryIsOnlyForTheAPIServer(t *testing.T) {
	f := newAggregatedFixture(t, true)
	if rec := f.serve(proxyRequest(aggregatedBase, nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	f.server.Trust = identity.NewRequestHeaderTrust(nil)
	if rec := f.serve(proxyRequest(aggregatedBase, nil)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status before the CA is loaded = %d, want 503", rec.Code)
	}
}

// TestAggregatedListenerOverRealTLS drives the handler through a real handshake: the API
// server's certificate gets through, any other is refused before a request is read.
func TestAggregatedListenerOverRealTLS(t *testing.T) {
	f := newAggregatedFixture(t, true)
	serving := identitytest.NewCA(t, "serving-ca").Issue(t, "localhost", x509.ExtKeyUsageServerAuth, time.Now().Add(time.Hour))
	srv := httptest.NewUnstartedServer(f.server.Handler())
	srv.TLS = f.server.Trust.ServerTLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &serving, nil }, false)
	srv.StartTLS()
	defer srv.Close()

	good := f.proxyCert(t)
	if code, body := tlsGet(t, srv, componentsPath, good); code != http.StatusOK || !strings.Contains(body, "operator") {
		t.Errorf("the API server's call: status = %d, body = %q", code, body)
	}
	rogue := identitytest.NewCA(t, "rogue-ca").Issue(t, proxyName, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	if code, _ := tlsGet(t, srv, componentsPath, &rogue); code != 0 {
		t.Errorf("a certificate of another CA got status %d, want a refused handshake", code)
	}
	if code, _ := tlsGet(t, srv, componentsPath, nil); code != 0 {
		t.Errorf("a client with no certificate got status %d, want a refused handshake", code)
	}
}

// tlsGet calls srv with the client certificate; a failed connection is status 0.
func tlsGet(t *testing.T, srv *httptest.Server, path string, cert *tls.Certificate) (int, string) {
	t.Helper()
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // the test server's own certificate
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("X-Remote-User", forwardedUser)
	resp, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}).Do(req)
	if err != nil {
		return 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestPlainListenerNeverReadsForwardedIdentityHeaders(t *testing.T) {
	s, asked := routesWithReviews(false, true)
	for path := range guardedPaths {
		rec := serve(s, path, "X-Remote-User", forgedUser, "X-Remote-Group", "system:masters")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, rec.Code)
		}
	}
	if asked.Spec.User != "" {
		t.Errorf("the forged user %q was asked about", asked.Spec.User)
	}
}
