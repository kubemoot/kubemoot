package api

import (
	"net/http"
	"regexp"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// WithAccess sets who verifies callers and who decides what a verified caller may do.
// Without it every caller is anonymous and nothing is checked, as before identity existed.
func (h *Handler) WithAccess(auth *identity.Authenticator, authz identity.Authorizer) *Handler {
	h.auth, h.authz = auth, authz
	return h
}

// ExposeLabel makes the gateway stamp a Kubernetes caller's user name on the principal of the
// questions it queues, so a crew that chose to tell its agents who is asking can. It is never
// stored: the conversation's owner record holds the id alone.
func (h *Handler) ExposeLabel(on bool) *Handler {
	h.exposeLabel = on
	return h
}

// enforcing reports whether this gateway verifies callers.
func (h *Handler) enforcing() bool { return h.auth != nil && h.auth.Enforcing() }

// guard authenticates the caller of an API route, and for a Kubernetes caller checks they
// may use the crew, then hands the verified caller to next in the request's context. A
// gateway that enforces nothing passes every request through unchanged.
func (h *Handler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.enforcing() {
			next(w, r)
			return
		}
		caller, err := h.auth.Authenticate(r)
		if err == nil && !caller.Anonymous() {
			err = h.mayUseCrew(r, caller)
		}
		if err != nil {
			if code, _ := identity.Status(err); code == http.StatusServiceUnavailable {
				handlerLog.Error(err, "Could not check the caller", "crew", r.PathValue("crew"))
			}
			refuse(w, err)
			return
		}
		next(w, r.WithContext(identity.WithCaller(r.Context(), caller)))
	}
}

// mayUseCrew asks whether a verified caller may use the crew in the request's path:
// get on that Crew. A review that could not be made refuses: it never allows.
func (h *Handler) mayUseCrew(r *http.Request, caller identity.Caller) error {
	if h.authz == nil {
		return identity.ErrForbidden
	}
	allowed, err := h.authz.Allowed(r.Context(), caller.Identity, identity.UseAccess(h.namespace, r.PathValue("crew")))
	if err != nil {
		return err
	}
	if !allowed {
		return identity.ErrForbidden
	}
	return nil
}

// refuse answers an authentication or authorization error. The cause of a failed check is
// logged by the caller, never sent.
func refuse(w http.ResponseWriter, err error) {
	code, message, challenge := identity.Refusal(err, "kubemoot-discussion-gateway")
	if challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	httpError(w, code, message)
}

// callerOf is the caller the guard verified for r, or the anonymous caller.
func callerOf(r *http.Request) identity.Caller { return identity.CallerFrom(r.Context()) }

// principalOf is the principal stamped on a request: the verified caller when this gateway
// verifies callers, and nothing otherwise, so an unconfigured crew's requests are
// unchanged. Any principal a client sent in the body is never read.
func (h *Handler) principalOf(r *http.Request) *bus.Principal {
	if !h.enforcing() {
		return nil
	}
	caller := callerOf(r)
	p := caller.Principal
	if h.exposeLabel && p.Kind == bus.PrincipalKindKubernetes && p.DisplayName == "" && caller.Identity != nil {
		p.DisplayName = labelOf(caller.Identity.Subject)
	}
	return &p
}

// maxLabelChars is the longest label stamped on a principal.
const maxLabelChars = 64

// labelPattern is what a label may hold: the characters of a Kubernetes user name or an email
// address. A name with anything else (a space, a quote, a bracket) is not stamped, so a label is
// always one plain token an agent's prompt can carry as data.
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9._@:/+=-]+$`)

// labelOf is the label of a Kubernetes user name: the name cut to maxLabelChars, or empty when
// it holds a character a label may not.
func labelOf(subject string) string {
	if len(subject) > maxLabelChars {
		subject = subject[:maxLabelChars]
	}
	if !labelPattern.MatchString(subject) {
		return ""
	}
	return subject
}
