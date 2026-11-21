package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/kubemoot/kubemoot/discussion-gateway/internal/grants"
)

// A person's own bearer token, for a crew whose verifier has passToTools, is held in memory
// for the turn so a tool can call the service as that person (see package grants). It is
// written when a verified subscriber asks, replaced by the renewal route, read only by the
// lease route (lease.go) and dropped when the turn ends, the conversation is stopped or
// deleted, or the token runs out. A Kubernetes caller never has one.

const (
	errCredentialNotKept = "this crew does not keep credentials for tools, or the caller's credential is not one it keeps"
	errGrantInUse        = "the conversation id is in use by another caller"
	errGrantFull         = "too many conversations hold a credential; ask again shortly"
	errTokenRanOut       = "the credential has expired"
)

// errGrantConflict means a live grant of another principal holds the conversation id.
var errGrantConflict = errors.New("another principal holds a grant for the conversation")

// errTokenExpired means the token has no expiry left to hold it for.
var errTokenExpired = errors.New("the token has no time left")

// credentialRenewal answers the renewal route. It carries the time the grant ends, never the token.
type credentialRenewal struct {
	ConversationID string `json:"conversationId"`
	ExpiresAt      string `json:"expiresAt"`
}

// WithGrants makes this gateway keep, for each verified subscriber whose verifier is one of
// passIssuers, the token they asked with. Without it no token is ever kept.
func (h *Handler) WithGrants(store grants.Store, passIssuers []string) *Handler {
	h.grants = store
	h.passIssuers = make(map[string]struct{}, len(passIssuers))
	for _, issuer := range passIssuers {
		h.passIssuers[issuer] = struct{}{}
	}
	return h
}

// keepsCredentialOf reports whether the caller's token is one this gateway keeps: a verified
// subscriber, vouched for by a verifier that passes tokens to tools. A Kubernetes caller,
// an anonymous one and a subscriber of any other verifier never are.
func (h *Handler) keepsCredentialOf(c identity.Caller) bool {
	if h.grants == nil || c.Anonymous() || c.Principal.Kind != bus.PrincipalKindSubscriber {
		return false
	}
	if c.Identity.Credential.Empty() {
		return false
	}
	_, passes := h.passIssuers[c.Identity.Issuer]
	return passes
}

// clock is the gateway's time, replaceable in a test.
func (h *Handler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// holdCredential keeps the caller's token for the conversation, when this gateway keeps it.
// keepLater leaves a held grant that outlives the new one in place (a renewal). It returns
// the time the grant in force ends. An error is errGrantConflict, grants.ErrFull or
// errTokenExpired.
func (h *Handler) holdCredential(c identity.Caller, conversationID string, keepLater bool) (time.Time, error) {
	expires, ok := grantEnd(h.clock(), c.Identity.Credential.Token())
	if !ok {
		return time.Time{}, errTokenExpired
	}
	g := grants.New(c.Principal, c.Identity.Issuer, c.Identity.Credential.Token(), expires)
	inForce := expires
	err := h.grants.PutIf(conversationID, g, replaceDecision(c.Principal, keepLater, &inForce))
	if err != nil {
		return time.Time{}, err
	}
	return inForce, nil
}

// grantEnd is when a grant of token written at now ends: the token's own expiry, or the
// ceiling, whichever is first. It is false for a token with no time left.
func grantEnd(now time.Time, token string) (time.Time, bool) {
	end := now.Add(grants.MaxLifetime)
	exp, ok := identity.TokenExpiry(token)
	if !ok || !exp.After(now) {
		return time.Time{}, false
	}
	if exp.Before(end) {
		end = exp
	}
	return end, true
}

// replaceDecision says whether a grant of owner ending at *inForce may replace the held one:
// never one of another principal, even an expired one, and with keepLater not one that ends
// later (inForce is then set to its end).
func replaceDecision(owner bus.Principal, keepLater bool, inForce *time.Time) func(grants.Grant, error) error {
	return func(held grants.Grant, heldErr error) error {
		found := heldErr == nil || errors.Is(heldErr, grants.ErrExpired)
		switch {
		case found && !identity.SamePrincipal(held.Owner, owner):
			return errGrantConflict
		case heldErr == nil && keepLater && !held.Expires.Before(*inForce):
			*inForce = held.Expires
			return grants.ErrKeepHeld
		}
		return nil
	}
}

// dropCredential forgets the conversation's grant.
func (h *Handler) dropCredential(conversationID string) {
	if h.grants != nil {
		h.grants.Delete(conversationID)
	}
}

// refuseGrant answers an error of holdCredential.
func refuseGrant(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errGrantConflict):
		httpError(w, http.StatusConflict, errGrantInUse)
	case errors.Is(err, grants.ErrFull):
		httpError(w, http.StatusServiceUnavailable, errGrantFull)
	case errors.Is(err, errTokenExpired):
		httpError(w, http.StatusUnauthorized, errTokenRanOut)
	default:
		httpError(w, http.StatusServiceUnavailable, "could not keep the credential")
	}
}

// holdForAsk keeps the asker's token before a question is queued, so the turn's first tool
// call finds it. It answers the refusal itself and returns false when the question must not
// be queued. A caller whose token is not kept passes untouched.
func (h *Handler) holdForAsk(w http.ResponseWriter, r *http.Request, conversationID string) bool {
	caller := callerOf(r)
	if !h.keepsCredentialOf(caller) {
		return true
	}
	if _, err := h.holdCredential(caller, conversationID, false); err != nil {
		refuseGrant(w, err)
		return false
	}
	return true
}

// renewCredential answers PUT /api/v1/discussions/{crew}/{conversationId}/credential: the
// caller's fresh token replaces the grant of a conversation they own, so a turn that outlasts
// the first token keeps going. Only the conversation's owner may renew it, and a token that
// ends earlier than the one held does not replace it.
func (h *Handler) renewCredential(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	id, ok := conversationIDOf(w, r)
	if !ok {
		return
	}
	caller := callerOf(r)
	if !h.keepsCredentialOf(caller) {
		httpError(w, http.StatusForbidden, errCredentialNotKept)
		return
	}
	if !h.isOwner(w, r, scope, id) {
		return
	}
	expires, err := h.holdCredential(caller, id, true)
	if err != nil {
		refuseGrant(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialRenewal{ConversationID: id, ExpiresAt: expires.UTC().Format(time.RFC3339)})
}

// isOwner answers 404 for a conversation with no owner record and 403 for one the caller does
// not own, and returns false after answering.
func (h *Handler) isOwner(w http.ResponseWriter, r *http.Request, scope crewscope.Scope, id string) bool {
	store, ok := h.ownerStore(w, scope)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), ownerLookupTimeout)
	defer cancel()
	owner, found, err := store.get(ctx, scope, id)
	switch {
	case err != nil:
		h.failedCheck(w, scope, err)
	case !found:
		httpError(w, http.StatusNotFound, errConversationNotFound)
	case !sameOwner(owner, callerOf(r).Principal):
		httpError(w, http.StatusForbidden, "only the person who started the conversation may renew its credential")
	default:
		return true
	}
	return false
}

// endGrantOnDone wraps a stream's emit so the conversation's grant is dropped when its
// synthesis or its done event goes out: the turn is over and no tool call needs the token.
// Only the grant's owner reading the stream ends it: an operator reading another person's
// conversation changes nothing.
func (h *Handler) endGrantOnDone(conversationID string, reader bus.Principal, emit func(SSEEvent)) func(SSEEvent) {
	if h.grants == nil {
		return emit
	}
	return func(e SSEEvent) {
		emit(e)
		if e.Type != bus.StreamEventBaseTypeSynthesis && e.Type != bus.StreamEventBaseTypeDone {
			return
		}
		if held, err := h.grants.Get(conversationID); err == nil && identity.SamePrincipal(held.Owner, reader) {
			h.dropCredential(conversationID)
		}
	}
}
