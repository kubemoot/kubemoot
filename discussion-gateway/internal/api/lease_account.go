package api

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// A lease for a connected account answers with the token the person connected at an outside
// service. The person is the owner the conversation records; the caller's claim of who asked
// must equal it and is never used in its place. The owner's id is already the install's keyed
// hash of the hop issuer and user name, which is the id the liaison keeps the person's
// connections under. The liaison's account broker decides the rest (the crew declares the account, the person consented to the crew).

// accountNamePattern is a DNS label, which is what a Crew's connected account name is.
var accountNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const maxAccountNameLength = 63

// accountLeaseTimeout bounds one lease: the owner read and the broker call.
const accountLeaseTimeout = 15 * time.Second

// The reason a lease could not be answered at all, which is retryable.
const reasonUnavailable = "UNAVAILABLE"

// OwnerReader reads who a conversation of this gateway's crew belongs to.
type OwnerReader interface {
	// Owner returns the conversation's owner; false when it has no record. An error means the
	// record could not be read.
	Owner(ctx context.Context, conversationID string) (bus.Principal, bool, error)
}

// WithAccounts lets the server lease connected account tokens: leaser asks the liaison's
// broker and owners finds the person a conversation belongs to.
func (l *LeaseServer) WithAccounts(leaser identity.AccountLeaser, owners OwnerReader) *LeaseServer {
	l.accounts, l.owners = leaser, owners
	return l
}

func validAccountName(name string) bool {
	return len(name) <= maxAccountNameLength && accountNamePattern.MatchString(name)
}

// leaseReason is the body of a lease that carries no token.
type leaseReason struct {
	Reason  string `json:"reason"`
	Account string `json:"account,omitempty"`
}

// accountLease answers a lease for a connected account. Every failure refuses, and no answer
// but the one success carries a token.
func (l *LeaseServer) accountLease(w http.ResponseWriter, r *http.Request, req leaseRequest) {
	if l.accounts == nil || l.owners == nil {
		leaseLog.Info("Refused an account lease: this gateway has no account broker", "account", req.Account)
		refuseLease(w, http.StatusServiceUnavailable, reasonUnavailable, req.Account)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accountLeaseTimeout)
	defer cancel()
	person, ok := l.leasePerson(ctx, w, req)
	if !ok {
		return
	}
	lease, err := l.accounts.Lease(ctx, person, req.Account)
	if refusal, refused := identity.IsLeaseRefusal(err); refused {
		leaseLog.Info("Refused an account lease", "account", req.Account, "reason", refusal.Reason)
		refuseLease(w, identity.LeaseStatusFor(refusal.Reason), refusal.Reason, req.Account)
		return
	}
	if err != nil {
		leaseLog.Info("An account lease could not be made; the broker gave no usable answer", "account", req.Account)
		refuseLease(w, http.StatusServiceUnavailable, reasonUnavailable, req.Account)
		return
	}
	if !lease.ExpiresAt.After(time.Now()) {
		refuseLease(w, http.StatusGone, identity.LeaseExpired, req.Account)
		return
	}
	leaseLog.V(1).Info("Served an account lease", "account", req.Account)
	writeJSON(w, http.StatusOK, leaseResponse{
		Token: lease.Token, ExpiresAt: lease.ExpiresAt.UTC().Format(time.RFC3339), TokenHosts: lease.TokenHosts,
	})
}

// leasePerson is the id the conversation's owner is connected under. The id the caller
// names (the principal the agent runtime put in the call's _meta, the same keyed hash the
// owner record holds) must be the owner's: a caller that knows only a conversation id gets
// nothing. It answers the refusal itself when there is none: a conversation with no owner, one
// whose owner is not a person the door signed in (an anonymous caller or a Kubernetes user has
// no connected accounts), or one owned by someone else.
func (l *LeaseServer) leasePerson(ctx context.Context, w http.ResponseWriter, req leaseRequest) (string, bool) {
	owner, found, err := l.owners.Owner(ctx, req.ConversationID)
	if err != nil {
		leaseLog.Info("An account lease could not read the conversation's owner", "error", err.Error())
		refuseLease(w, http.StatusServiceUnavailable, reasonUnavailable, req.Account)
		return "", false
	}
	if !found || owner.Kind != bus.PrincipalKindSubscriber || owner.ID == "" || owner.ID != req.PrincipalID {
		leaseLog.Info("Refused an account lease: the conversation is not the principal's", "account", req.Account)
		refuseLease(w, identity.LeaseStatusFor(identity.LeaseNoGrant), identity.LeaseNoGrant, req.Account)
		return "", false
	}
	return owner.ID, true
}

func refuseLease(w http.ResponseWriter, status int, reason, account string) {
	writeJSON(w, status, leaseReason{Reason: reason, Account: account})
}
