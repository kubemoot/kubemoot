package signin

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const maxRedirectURIs = 10

// registration is what a client id seals.
type registration struct {
	RedirectURIs []string `json:"r"`
	Name         string   `json:"n,omitempty"`
	IssuedAt     int64    `json:"t"`
}

// register is dynamic client registration (RFC 7591) for public clients. The client id is
// the sealed registration, so nothing is stored.
func (f *front) register(w http.ResponseWriter, r *http.Request) {
	var req oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is not valid client metadata")
		return
	}
	if msg := f.checkRedirectURIs(req.RedirectURIs); msg != "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", msg)
		return
	}
	issued := f.now()
	id, err := f.sealer.Seal(purposeClient, registration{
		RedirectURIs: req.RedirectURIs, Name: truncate(req.ClientName, 100), IssuedAt: issued.Unix(),
	})
	if err != nil {
		f.event("register failed")
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	f.event("client registered")
	resp := &oauthex.ClientRegistrationResponse{
		ClientID:         id,
		ClientIDIssuedAt: time.Unix(issued.Unix(), 0),
		ClientRegistrationMetadata: oauthex.ClientRegistrationMetadata{
			RedirectURIs:            req.RedirectURIs,
			ClientName:              truncate(req.ClientName, 100),
			TokenEndpointAuthMethod: authMethodNone,
			GrantTypes:              []string{grantAuthCode, grantRefresh},
			ResponseTypes:           []string{responseCode},
		},
	}
	writeJSON(w, http.StatusCreated, resp)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// checkRedirectURIs returns why the list is refused, or "".
func (f *front) checkRedirectURIs(uris []string) string {
	if len(uris) == 0 || len(uris) > maxRedirectURIs {
		return "give between one and ten redirect URIs"
	}
	for _, raw := range uris {
		if !f.redirectAllowed(raw) {
			return "a redirect URI must be https (or http on localhost), without a fragment, on an allowed host"
		}
	}
	return ""
}

func (f *front) redirectAllowed(raw string) bool {
	u, ok := validWebURL(raw)
	if !ok {
		return false
	}
	if isLoopback(u.Hostname()) && u.Scheme == "http" {
		return true
	}
	return len(f.cfg.AllowedRedirectHosts) == 0 || slices.Contains(f.cfg.AllowedRedirectHosts, u.Hostname())
}
