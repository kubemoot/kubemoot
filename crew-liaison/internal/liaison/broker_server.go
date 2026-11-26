package liaison

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
)

// AccountBrokerAudience is the audience of the ServiceAccount token a crew's discussion
// gateway presents to the account broker.
const AccountBrokerAudience = "kubemoot-account-broker"

const (
	maxBrokerRequestBytes = 2048
	brokerReviewTimeout   = 10 * time.Second
	// gatewayAccountSuffix is the end of the name of a crew's discussion gateway ServiceAccount.
	gatewayAccountSuffix = "-discussion"
	serviceAccountPrefix = "system:serviceaccount:"
)

var brokerLog = logf.Log.WithName("account-broker")

// brokerReviewer checks the caller's ServiceAccount token; identity.Reviewer is the one in use.
type brokerReviewer interface {
	Review(ctx context.Context, token string) (*identity.Identity, error)
}

// AccountBroker is the internal route of connected accounts, served on its own port and never
// by the public mux. The one caller is a crew's discussion gateway, which resolves the person
// from the conversation and asks for that person's token or whether they are connected.
//
// The crew is read from the caller's ServiceAccount, never from the request: a gateway can
// ask only for the crew it serves.
type AccountBroker struct {
	broker   *accounts.Broker
	reviewer brokerReviewer
	gateways GatewayVerifier
}

// GatewayVerifier says whether a ServiceAccount is the one the operator made for a crew's
// discussion gateway. The name alone proves nothing: anyone who can create a ServiceAccount in a
// namespace can pick any name in it.
type GatewayVerifier interface {
	// Verify reports whether serviceAccount in namespace belongs to the crew's gateway. An error
	// means it could not be checked, which refuses and is retryable.
	Verify(ctx context.Context, namespace, serviceAccount, crew string) (bool, error)
}

// NewAccountBroker returns the internal handler. reviewer must check tokens against
// AccountBrokerAudience.
func NewAccountBroker(broker *accounts.Broker, reviewer brokerReviewer, gateways GatewayVerifier) *AccountBroker {
	return &AccountBroker{broker: broker, reviewer: reviewer, gateways: gateways}
}

// Handler serves the broker's routes.
func (a *AccountBroker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/accounts/lease", a.lease)
	mux.HandleFunc("POST /internal/v1/accounts/connected", a.connected)
	return mux
}

type brokerRequest struct {
	PrincipalID string `json:"principalId"`
	Account     string `json:"account"`
}

// leaseAnswer is the one response that carries a token.
type leaseAnswer struct {
	Token      string   `json:"token"`
	ExpiresAt  string   `json:"expiresAt"`
	TokenHosts []string `json:"tokenHosts"`
}

type reasonAnswer struct {
	Reason string `json:"reason,omitempty"`
	// Connected is set by the connected route.
	Connected *bool  `json:"connected,omitempty"`
	Account   string `json:"account,omitempty"`
}

func (a *AccountBroker) lease(w http.ResponseWriter, r *http.Request) {
	crew, req, ok := a.begin(w, r)
	if !ok {
		return
	}
	l, err := a.broker.Lease(r.Context(), req.PrincipalID, req.Account, crew)
	var refusal *accounts.LeaseError
	switch {
	case errors.As(err, &refusal):
		writeBrokerJSON(w, refusalStatus(refusal.Reason), reasonAnswer{Reason: refusal.Reason, Account: req.Account})
	case err != nil:
		brokerLog.Info("A lease could not be made", "account", req.Account)
		http.Error(w, "the account could not be leased", http.StatusServiceUnavailable)
	default:
		w.Header().Set("Cache-Control", "no-store")
		writeBrokerJSON(w, http.StatusOK, leaseAnswer{
			Token: l.Token, ExpiresAt: l.ExpiresAt.UTC().Format(time.RFC3339), TokenHosts: l.TokenHosts,
		})
	}
}

func (a *AccountBroker) connected(w http.ResponseWriter, r *http.Request) {
	crew, req, ok := a.begin(w, r)
	if !ok {
		return
	}
	reason, err := a.broker.Check(r.Context(), req.PrincipalID, req.Account, crew)
	if err != nil {
		http.Error(w, "the account could not be checked", http.StatusServiceUnavailable)
		return
	}
	live := reason == ""
	writeBrokerJSON(w, http.StatusOK, reasonAnswer{Reason: reason, Connected: &live, Account: req.Account})
}

func refusalStatus(reason string) int {
	switch reason {
	case accounts.ReasonNeedsReconnect:
		return http.StatusConflict
	case accounts.ReasonExpired:
		return http.StatusGone
	}
	return http.StatusNotFound
}

// begin authenticates the caller, derives its crew and reads the request, answering the
// refusal itself.
func (a *AccountBroker) begin(w http.ResponseWriter, r *http.Request) (string, brokerRequest, bool) {
	w.Header().Set("Cache-Control", "no-store")
	crew, ok := a.authenticate(w, r)
	if !ok {
		return "", brokerRequest{}, false
	}
	var req brokerRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBrokerRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.PrincipalID == "" || req.Account == "" {
		http.Error(w, "principalId and account are required", http.StatusBadRequest)
		return "", brokerRequest{}, false
	}
	return crew, req, true
}

// authenticate checks the ServiceAccount token and returns the crew ("namespace/name") whose
// discussion gateway it belongs to.
func (a *AccountBroker) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), brokerReviewTimeout)
	defer cancel()
	id, err := a.reviewer.Review(ctx, identity.BearerToken(r))
	switch {
	case errors.Is(err, identity.ErrNoCredential), errors.Is(err, identity.ErrInvalid):
		w.Header().Set("WWW-Authenticate", `Bearer realm="kubemoot-account-broker"`)
		http.Error(w, "a ServiceAccount token for the audience "+AccountBrokerAudience+" is required",
			http.StatusUnauthorized)
		return "", false
	case err != nil:
		http.Error(w, "could not check the caller", http.StatusServiceUnavailable)
		return "", false
	}
	crew, ok := crewOfGateway(id.Subject)
	if ok {
		ok, err = a.ownsGateway(ctx, id.Subject, crew)
	}
	switch {
	case err != nil:
		brokerLog.Info("Could not check the caller's ServiceAccount")
		http.Error(w, "could not check the caller", http.StatusServiceUnavailable)
		return "", false
	case !ok:
		brokerLog.Info("Refused a caller that is not a crew's discussion gateway")
		http.Error(w, "this caller may not ask for connected accounts", http.StatusForbidden)
		return "", false
	}
	return crew, true
}

// crewOfGateway reads the crew out of a discussion gateway's ServiceAccount user name,
// system:serviceaccount:<namespace>:<crew>-discussion.
func crewOfGateway(subject string) (string, bool) {
	rest, ok := strings.CutPrefix(subject, serviceAccountPrefix)
	if !ok {
		return "", false
	}
	ns, name, found := strings.Cut(rest, ":")
	crew, hasSuffix := strings.CutSuffix(name, gatewayAccountSuffix)
	if !found || !hasSuffix || ns == "" || crew == "" || strings.ContainsAny(name, ":/") {
		return "", false
	}
	return ns + "/" + crew, true
}

func writeBrokerJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ownsGateway asks the verifier whether the ServiceAccount named by subject is the crew's gateway's.
func (a *AccountBroker) ownsGateway(ctx context.Context, subject, crew string) (bool, error) {
	ns, name, _ := strings.Cut(strings.TrimPrefix(subject, serviceAccountPrefix), ":")
	return a.gateways.Verify(ctx, ns, name, strings.TrimPrefix(crew, ns+"/"))
}
