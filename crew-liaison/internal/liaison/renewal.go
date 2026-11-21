package liaison

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// renewTimeout bounds one renewal call to the crew's gateway.
const renewTimeout = 10 * time.Second

// handoff carries a caller's token from the call that started a discussion to the follower
// that opens its stream, and nowhere else. The follower takes the token once, when it builds
// the stream request, and the handoff is empty from then on, so the liaison holds no token
// while the discussion runs.
type handoff struct {
	mu    sync.Mutex
	token string
}

// newHandoff holds token until it is taken. An empty token is a handoff of nothing.
func newHandoff(token string) *handoff { return &handoff{token: token} }

// take returns the token and forgets it. A nil handoff, or a second call, returns "".
func (h *handoff) take() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	token := h.token
	h.token = ""
	return token
}

// held reports whether the handoff still holds a token.
func (h *handoff) held() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.token != ""
}

// fingerprint is a keyed digest of a token. A ticket remembers the fingerprint of the last
// token sent for it, never the token, so the liaison can tell a fresh token from the same one
// without keeping either. The key is random per process and never leaves it.
type fingerprint [sha256.Size]byte

// fingerprinter makes fingerprints under one random key.
type fingerprinter struct{ key []byte }

func newFingerprinter() fingerprinter {
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		panic("liaison: no system randomness for a token fingerprint: " + err.Error())
	}
	return fingerprinter{key: key}
}

func (f fingerprinter) of(token string) fingerprint {
	m := hmac.New(sha256.New, f.key)
	_, _ = m.Write([]byte(token))
	var out fingerprint
	copy(out[:], m.Sum(nil))
	return out
}

// sentCredential reports whether fp is the fingerprint of the last token sent for the
// ticket.
func (t *Ticket) sentCredential(fp fingerprint) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sentSet && subtle.ConstantTimeCompare(t.sent[:], fp[:]) == 1
}

// rememberCredential records fp as the fingerprint of the last token sent for the ticket.
func (t *Ticket) rememberCredential(fp fingerprint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = fp
	t.sentSet = true
}

// renewableToken is the token to keep alive at the crew's gateway for the caller in ctx: the
// token a service verified for a person. A Kubernetes token, a shared token and an anonymous
// caller have none, so nothing is renewed for them.
func renewableToken(ctx context.Context) string {
	caller := identity.CallerFrom(ctx)
	if caller.Anonymous() {
		return ""
	}
	return caller.Identity.Credential.Token()
}

// recordStartCredential notes the token the start call presented, so the first get_answer
// carrying the same token does not send it again.
func (s *Service) recordStartCredential(ctx context.Context, ticket *Ticket) {
	if token := renewableToken(ctx); token != "" {
		ticket.rememberCredential(s.tickets.fingerprints.of(token))
	}
}

// renew hands the gateway the caller's token when it differs from the last one sent for the
// ticket, so a turn that outlasts its first token keeps its credential. The caller is the
// ticket's owner (the caller checked) and the gateway checks it again. Only a service token
// is renewed; a Kubernetes token is still presented on the question and the stream (see
// credentialFrom) but the gateway keeps no grant for it. A failure is logged
// without the token and retried on the next poll: the answer does not depend on it.
func (s *Service) renew(ctx context.Context, ticket *Ticket) {
	token := renewableToken(ctx)
	if token == "" || ticket.Snapshot().State != StatePending {
		return
	}
	ticket.renewMu.Lock()
	defer ticket.renewMu.Unlock()
	fp := s.tickets.fingerprints.of(token)
	if ticket.sentCredential(fp) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, renewTimeout)
	defer cancel()
	if err := s.gateway.Renew(ctx, ticket.Namespace, ticket.Crew, ticket.Conversation, token); err != nil {
		log.Error(err, "could not renew the asker's credential", "ticket", ticket.ID, "crew", ticket.Crew)
		return
	}
	ticket.rememberCredential(fp)
}

// Renew sends the gateway a fresh token for a conversation the caller owns. Only the status
// of the answer is read, and it is never put in an error with the token.
func (g *Gateway) Renew(ctx context.Context, namespace, crew, conversation, token string) error {
	url := g.baseURL(namespace, crew) + "/" + conversation + "/credential"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("crew %s/%s gateway unreachable: %w", namespace, crew, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("crew %s/%s gateway refused the renewed credential: %s", namespace, crew, resp.Status)
	}
	return nil
}
