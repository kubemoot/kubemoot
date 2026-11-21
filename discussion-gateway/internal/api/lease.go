package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/grants"
)

// The lease is how a crew's MCP gateway gets the token of the person whose question a tool
// call serves: it names the conversation and the principal the thread was started for, proves
// it is the MCP gateway with its projected ServiceAccount token, and gets that person's token
// for one upstream call. The route is served only on its own internal port, never by the
// public mux, and is the one place a token leaves the gateway.

// BrokerAudience is the audience of the ServiceAccount token the MCP gateway presents.
const BrokerAudience = "kubemoot-credential-broker"

const (
	maxLeaseRequestBytes = 4096
	reviewTimeout        = 10 * time.Second
)

var leaseLog = logf.Log.WithName("lease")

// leaseReviewer checks the caller's ServiceAccount token; identity.Reviewer is the one in use.
type leaseReviewer interface {
	Review(ctx context.Context, token string) (*identity.Identity, error)
}

// LeaseServer answers lease requests from the one ServiceAccount allowed to ask.
type LeaseServer struct {
	store    grants.Store
	reviewer leaseReviewer
	// subject is the full user name of the ServiceAccount allowed to lease, such as
	// system:serviceaccount:<namespace>:<name>.
	subject string
}

// NewLeaseServer returns a lease server over store. reviewer must check tokens against
// BrokerAudience; only the ServiceAccount named subject is served.
func NewLeaseServer(store grants.Store, reviewer leaseReviewer, subject string) *LeaseServer {
	return &LeaseServer{store: store, reviewer: reviewer, subject: subject}
}

// RegisterRoutes registers the lease route on the internal mux.
func (l *LeaseServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/lease", l.lease)
}

// leaseRequest names the turn the token is for.
type leaseRequest struct {
	ConversationID string `json:"conversationId"`
	PrincipalID    string `json:"principalId"`
	Verifier       string `json:"verifier"`
}

// leaseResponse is the one response that carries a token.
type leaseResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

func (l *LeaseServer) lease(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !l.authenticate(w, r) {
		return
	}
	req, ok := readLeaseRequest(w, r)
	if !ok {
		return
	}
	g, err := l.store.Get(req.ConversationID)
	if errors.Is(err, grants.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no credential is held for the conversation")
		return
	}
	if !matches(g, req) {
		leaseLog.Info("Refused a lease: the principal does not match", "conversationId", req.ConversationID)
		httpError(w, http.StatusForbidden, "the principal does not match the conversation")
		return
	}
	if errors.Is(err, grants.ErrExpired) {
		httpError(w, http.StatusGone, "the credential has expired")
		return
	}
	leaseLog.Info("Served a lease", "conversationId", req.ConversationID, "principal", g.Owner.ID, "verifier", g.Verifier)
	writeJSON(w, http.StatusOK, leaseResponse{Token: g.Token(), ExpiresAt: g.Expires.UTC().Format(time.RFC3339)})
}

// matches reports whether the request names the grant's owner and verifier.
func matches(g grants.Grant, req leaseRequest) bool {
	asked := bus.Principal{Kind: bus.PrincipalKindSubscriber, ID: req.PrincipalID}
	return identity.SamePrincipal(g.Owner, asked) && g.Verifier == req.Verifier
}

// authenticate checks the caller's ServiceAccount token and that it is the one allowed. It
// answers the refusal itself.
func (l *LeaseServer) authenticate(w http.ResponseWriter, r *http.Request) bool {
	ctx, cancel := context.WithTimeout(r.Context(), reviewTimeout)
	defer cancel()
	id, err := l.reviewer.Review(ctx, identity.BearerToken(r))
	switch {
	case errors.Is(err, identity.ErrNoCredential), errors.Is(err, identity.ErrInvalid):
		w.Header().Set("WWW-Authenticate", `Bearer realm="kubemoot-credential-broker"`)
		httpError(w, http.StatusUnauthorized, "a ServiceAccount token for the audience "+BrokerAudience+" is required")
	case err != nil:
		leaseLog.Error(err, "Could not check the lease caller")
		httpError(w, http.StatusServiceUnavailable, "could not check the caller")
	case l.subject == "" || id.Subject != l.subject:
		leaseLog.Info("Refused a lease: not the ServiceAccount allowed to ask")
		httpError(w, http.StatusForbidden, "this caller may not lease credentials")
	default:
		return true
	}
	return false
}

// readLeaseRequest decodes and validates the body, answering 400 itself.
func readLeaseRequest(w http.ResponseWriter, r *http.Request) (leaseRequest, bool) {
	var req leaseRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLeaseRequestBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(&req)
	if err != nil || req.PrincipalID == "" || req.Verifier == "" || !validConversationID(req.ConversationID) {
		httpError(w, http.StatusBadRequest, "conversationId, principalId and verifier are required")
		return req, false
	}
	return req, true
}
