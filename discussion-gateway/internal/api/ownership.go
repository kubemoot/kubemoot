package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"github.com/nats-io/nats.go/jetstream"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// A conversation belongs to the principal that started it. On a crew that verifies callers,
// the owner or an operator may act on it. An operator is a caller the API server says may
// get (to read one) or list (to list all) the crew's virtual crews/conversations resource,
// and must say why in the Kubemoot-Access-Reason header; every such access is audited.
// A crew that verifies no one has no owners and these checks are skipped.

const (
	// errConversationNotFound is the one answer for a conversation that does not exist and
	// one the caller may not see, so an id cannot be probed.
	errConversationNotFound = "conversation not found"
)

// The operations an audit record names.
const (
	opStream    = "stream"
	opWorkspace = "workspace"
	opList      = "list"
)

// auditLog receives the audit records; a test replaces it.
var auditLog logr.Logger = logf.Log.WithName("audit")

// ownerLookupTimeout bounds the owner reads and writes of one request.
const ownerLookupTimeout = 5 * time.Second

// ownerStore reads and writes owner records in the conversations bucket, over a bucket
// resolved once for the request.
type ownerStore struct {
	kv jetstream.KeyValue
}

// ownerStore opens the conversations bucket, and answers 503 itself when it cannot.
func (h *Handler) ownerStore(w http.ResponseWriter, scope crewscope.Scope) (ownerStore, bool) {
	js, err := h.natsClient.JetStream()
	if err == nil {
		var kv jetstream.KeyValue
		if kv, err = js.KeyValue(context.Background(), bus.ConversationsBucket); err == nil {
			return ownerStore{kv: kv}, true
		}
	}
	h.failedCheck(w, scope, err)
	return ownerStore{}, false
}

// sameOwner reports whether the principal is the one the record names.
func sameOwner(owner, caller bus.Principal) bool { return identity.SamePrincipal(owner, caller) }

// get returns the conversation's owner, and false when it has no record. A record that
// cannot be read owns nothing, so the conversation is the operator's, and is logged.
func (o ownerStore) get(ctx context.Context, scope crewscope.Scope, id string) (bus.Principal, bool, error) {
	entry, err := o.kv.Get(ctx, bus.ConversationOwnerKey(scope.Namespace, scope.Crew, id))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return bus.Principal{}, false, nil
	}
	if err != nil {
		return bus.Principal{}, false, err
	}
	owner, ok := bus.DecodeConversationOwner(entry.Value())
	if !ok {
		handlerLog.Info("An owner record could not be read; the conversation is operator-only", "crew", scope.Crew)
	}
	return owner, ok, nil
}

// claim records the caller as the owner of a new conversation, or refreshes the record of
// the caller's own, and reports whether the caller may ask on it. A conversation owned by
// another caller, and one whose history exists but whose record is gone, are refused: an id
// that is not the caller's cannot attach to someone's history.
func (o ownerStore) claim(ctx context.Context, scope crewscope.Scope, id string, caller bus.Principal) (bool, error) {
	value, err := json.Marshal(bus.ConversationOwner{Owner: caller})
	if err != nil {
		return false, err
	}
	// Two attempts: a Create that loses a race to another claim reads the winner's record.
	for range 2 {
		done, ok, err := o.claimOnce(ctx, scope, id, caller, value)
		if done || err != nil {
			return ok, err
		}
	}
	return false, nil
}

// claimOnce is one attempt to claim. done is false only when Create lost a race.
func (o ownerStore) claimOnce(
	ctx context.Context, scope crewscope.Scope, id string, caller bus.Principal, value []byte,
) (done, ok bool, err error) {
	key := bus.ConversationOwnerKey(scope.Namespace, scope.Crew, id)
	owner, found, err := o.get(ctx, scope, id)
	if err != nil {
		return true, false, err
	}
	if found {
		if !sameOwner(owner, caller) {
			return true, false, nil
		}
		_, err = o.kv.Put(ctx, key, value)
		return true, err == nil, err
	}
	if orphan, err := o.historyExists(ctx, scope, id); err != nil || orphan {
		return true, false, err
	}
	_, err = o.kv.Create(ctx, key, value)
	if errors.Is(err, jetstream.ErrKeyExists) {
		return false, false, nil
	}
	return true, err == nil, err
}

// historyExists reports whether the conversation's history is still held.
func (o ownerStore) historyExists(ctx context.Context, scope crewscope.Scope, id string) (bool, error) {
	_, err := o.kv.Get(ctx, bus.ConversationHistoryKey(scope.Namespace, scope.Crew, id))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, nil
	}
	return err == nil, err
}

// mayAsk records ownership for a question and answers the refusal itself. A caller who is
// not the owner is answered as for an unknown conversation, including an operator: an
// operator reads other callers' conversations, and does not add turns to them.
func (h *Handler) mayAsk(w http.ResponseWriter, r *http.Request, scope crewscope.Scope, id string) bool {
	if !h.enforcing() {
		return true
	}
	store, ok := h.ownerStore(w, scope)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), ownerLookupTimeout)
	defer cancel()
	claimed, err := store.claim(ctx, scope, id, callerOf(r).Principal)
	switch {
	case err != nil:
		h.failedCheck(w, scope, err)
		return false
	case !claimed:
		httpError(w, http.StatusNotFound, errConversationNotFound)
		return false
	}
	return true
}

// mayRead lets the owner, or an operator with a reason, read a conversation, and answers the
// refusal itself.
func (h *Handler) mayRead(w http.ResponseWriter, r *http.Request, scope crewscope.Scope, id, op string) bool {
	if !h.enforcing() {
		return true
	}
	caller := callerOf(r)
	store, ok := h.ownerStore(w, scope)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), ownerLookupTimeout)
	defer cancel()
	owner, found, err := store.get(ctx, scope, id)
	if err != nil {
		h.failedCheck(w, scope, err)
		return false
	}
	if found && sameOwner(owner, caller.Principal) {
		return true
	}
	return h.operatorAccess(w, r, scope, "get", op, id, owner.ID)
}

// operatorAccess admits a caller who may use verb on the crew's conversations, when they give
// a reason, and audits it. Everyone else is answered as for an unknown conversation.
func (h *Handler) operatorAccess(
	w http.ResponseWriter, r *http.Request, scope crewscope.Scope, verb, op, id, ownerID string,
) bool {
	caller := callerOf(r)
	if caller.Anonymous() || h.authz == nil {
		httpError(w, http.StatusNotFound, errConversationNotFound)
		return false
	}
	attrs := identity.ConversationsAccess(scope.Namespace, scope.Crew, verb)
	allowed, err := h.authz.Allowed(r.Context(), caller.Identity, attrs)
	if err != nil {
		h.failedCheck(w, scope, err)
		return false
	}
	if !allowed {
		httpError(w, http.StatusNotFound, errConversationNotFound)
		return false
	}
	reason, ok := identity.ValidateAccessReason(r.Header.Get(identity.HeaderAccessReason))
	if !ok {
		httpError(w, http.StatusBadRequest,
			"reading another caller's conversations needs a reason of 1 to 200 characters in the "+identity.HeaderAccessReason+" header")
		return false
	}
	identity.OperatorAccess{Operator: caller.Identity, Operation: op, Namespace: scope.Namespace, Crew: scope.Crew,
		ConversationID: id, OwnerID: ownerID, Reason: reason}.Audit(auditLog)
	return true
}

// failedCheck answers a check that could not be made: 503, with the cause only in the log.
func (h *Handler) failedCheck(w http.ResponseWriter, scope crewscope.Scope, err error) {
	handlerLog.Error(err, "Could not check access to a conversation", "crew", scope.Crew)
	httpError(w, http.StatusServiceUnavailable, "could not check the caller's access")
}

// listVisibility is what a list shows the caller: with all=true an operator sees every
// conversation, with a reason and an audit record, else the caller sees their own. It
// returns false after answering a refusal. A gateway that verifies no one lists everything.
// failed reports the first owner lookup that could not be made, which the caller answers 503:
// a list never silently drops conversations it could not check.
func (h *Handler) listVisibility(
	w http.ResponseWriter, r *http.Request, scope crewscope.Scope,
) (visible visibility, failed func() error, ok bool) {
	if !h.enforcing() {
		return nil, func() error { return nil }, true
	}
	want := func(owner bus.Principal) bool { return sameOwner(owner, callerOf(r).Principal) }
	if r.URL.Query().Get("all") == "true" {
		if !h.operatorAccess(w, r, scope, "list", opList, "", "") {
			return nil, nil, false
		}
		want = func(bus.Principal) bool { return true }
	}
	store, ok := h.ownerStore(w, scope)
	if !ok {
		return nil, nil, false
	}
	var first error
	ctx, cancel := context.WithTimeout(r.Context(), listReadTimeout)
	context.AfterFunc(r.Context(), cancel)
	visible = func(id string) (string, bool) {
		owner, found, err := store.get(ctx, scope, id)
		if err != nil {
			if first == nil {
				first = err
			}
			return "", false
		}
		return owner.ID, found && want(owner)
	}
	return visible, func() error { cancel(); return first }, true
}
