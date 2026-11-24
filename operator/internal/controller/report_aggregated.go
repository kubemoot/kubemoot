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
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// The aggregated API the operator serves its reports under. The API server authenticates
// whoever calls it, with whatever their kubeconfig holds (a client certificate, OIDC, a
// token), and forwards the result to this server over mutual TLS, so the caller's own RBAC
// decides what they read with no token for the operator to check.
const (
	ReportsAPIGroup   = "reports.kubemoot.ai"
	ReportsAPIVersion = "v1alpha1"

	aggregatedBase = "/apis/" + ReportsAPIGroup + "/" + ReportsAPIVersion

	// frontProxyRefresh is how often the API server's front proxy CA is read again, so a
	// rotated CA is trusted within this time.
	frontProxyRefresh = time.Minute
)

// aggregatedPaths maps each report service route to its path under the aggregated API:
// the route table's pattern to a path below aggregatedBase. A route with no entry is not
// served on the aggregated listener, so a new endpoint is never exposed there by accident
// (a test checks that every route has an entry).
var aggregatedPaths = map[string]string{
	"GET /componentstatuses":                      "/componentstatuses",
	"GET /memory-export/{namespace}/{crew}":       "/namespaces/{namespace}/crews/{crew}/memory-export",
	"GET /conversation-export/{namespace}/{crew}": "/namespaces/{namespace}/crews/{crew}/conversation-export",
	"GET /report/{namespace}/{name}":              "/namespaces/{namespace}/crewfitnesssuites/{name}/report",
}

// errNoForwardedCaller answers a request the API server did not forward a caller on.
var errNoForwardedCaller = errors.New("the request carries no caller the API server authenticated")

// reportsDiscovery is the APIResourceList of the aggregated API. The reports are
// subresources of the objects they are built from, and componentstatuses stands alone.
func reportsDiscovery() *metav1.APIResourceList {
	get := metav1.Verbs{"get"}
	return &metav1.APIResourceList{
		TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
		GroupVersion: ReportsAPIGroup + "/" + ReportsAPIVersion,
		APIResources: []metav1.APIResource{
			{Name: "crews/memory-export", Namespaced: true, Kind: "MemoryExport", Verbs: get},
			{Name: "crews/conversation-export", Namespaced: true, Kind: "ConversationExport", Verbs: get},
			{Name: "crewfitnesssuites/report", Namespaced: true, Kind: "FitnessReport", Verbs: get},
			{Name: "componentstatuses", Namespaced: false, Kind: "ComponentStatusList", Verbs: get},
		},
	}
}

// AggregatedReportServer serves the report service's endpoints as an aggregated API on a
// second TLS listener. Only the API server can connect: the listener verifies the client
// certificate against the CA the cluster publishes for its front proxy, and the caller is
// whoever the API server says it authenticated, read by identity.RequestHeaderVerifier.
// Authorization is the report service's own, the SubjectAccessReview of the caller.
type AggregatedReportServer struct {
	// Reports holds the handlers and the authorizer; the plain listener serves the same.
	Reports *ReportServer
	// Trust holds the front proxy CA and header names, and is kept fresh by Start.
	Trust *identity.RequestHeaderTrust
	// Client reads the front proxy ConfigMap.
	Client kubernetes.Interface
	// Certificate is the serving certificate, reloaded as it rotates.
	Certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	Addr        string
	// EnableHTTP2 serves HTTP/2, as the operator's other listeners do only when asked.
	EnableHTTP2 bool
}

// NeedLeaderElection is false: every replica serves, so the API server's calls through the
// Service reach a working pod whichever it picks.
func (*AggregatedReportServer) NeedLeaderElection() bool { return false }

// Handler is the aggregated listener's handler: discovery, and the reports behind the gate.
func (a *AggregatedReportServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+aggregatedBase, a.serveDiscovery)
	for _, rt := range a.Reports.routeTable() {
		path, ok := aggregatedPaths[rt.pattern]
		if !ok {
			continue
		}
		// A subscriber has no Kubernetes identity for the API server to forward, so only
		// Kubernetes callers reach these routes.
		mux.HandleFunc("GET "+aggregatedBase+path, a.Reports.gateWith(a.check, rt.access, false, rt.handler))
	}
	return mux
}

// serveDiscovery answers the API server's discovery of this API, which it also uses to
// decide the APIService is available. It carries no report data and names no caller, so
// it needs only that the API server asked.
func (a *AggregatedReportServer) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	ok, err := identity.RequestHeaderVerifier{Trust: a.Trust}.FromFrontProxy(r)
	switch {
	case err != nil:
		http.Error(w, "the front proxy configuration is not loaded", http.StatusServiceUnavailable)
	case !ok:
		http.Error(w, errNoForwardedCaller.Error(), http.StatusUnauthorized)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reportsDiscovery())
	}
}

// check identifies the caller the API server forwarded and asks whether they may make the
// access. A request that did not come through the API server's verified certificate is
// unauthenticated, whatever headers it carries; a caller whose RBAC lacks the access is
// forbidden. With no authorizer the gate has already failed closed.
func (a *AggregatedReportServer) check(
	r *http.Request, attrs authorizationv1.ResourceAttributes, _ bool,
) (*identity.Identity, error) {
	id, err := identity.RequestHeaderVerifier{Trust: a.Trust}.Verify(r)
	switch {
	case errors.Is(err, identity.ErrNoCredential), errors.Is(err, identity.ErrInvalid):
		return nil, errNoForwardedCaller
	case err != nil:
		return nil, err
	}
	return allowedIdentity(r.Context(), a.Reports.Auth, id, attrs)
}

// Start implements manager.Runnable: it keeps the front proxy CA fresh and serves until ctx ends.
func (a *AggregatedReportServer) Start(ctx context.Context) error {
	go a.Trust.Refresh(ctx, a.Client, frontProxyRefresh, func(err error) {
		reportLog.Error(err, "could not read the API server's front proxy configuration; keeping the last good one")
	})
	srv := &http.Server{
		Addr: a.Addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 16,
		TLSConfig: a.Trust.ServerTLSConfig(a.Certificate, a.EnableHTTP2),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	reportLog.Info("Aggregated report API listening", "addr", a.Addr, "group", ReportsAPIGroup+"/"+ReportsAPIVersion)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
