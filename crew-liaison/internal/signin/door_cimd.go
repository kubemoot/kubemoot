package signin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/kubemoot/kubemoot/operator/pkg/netguard"
)

// Client ID metadata documents (draft-ietf-oauth-client-id-metadata-document): a chat app's
// client_id is an https address, and the document at that address says who the app is and where
// it may be sent back. The door fetches it, so the address is an input an attacker controls and
// the fetch is the one place the door makes a request to a stranger's server. Every guard here
// is about that: only https, only the public internet (checked again at connect time), no
// redirect, a small body, a short time, and a document that names itself and no secret.
const (
	maxMetadataBytes = 16 << 10
	metadataWait     = 5 * time.Second
	fetchRate        = rate.Limit(5)
	fetchBurst       = 10
	maxClientIDChars = 2048
)

var (
	errBadClientID = errors.New("the client id is not an acceptable address")
	errFetchBusy   = errors.New("too many client metadata fetches")
	errBadMetadata = errors.New("the client metadata document is not acceptable")
)

// metadataFetcher fetches and checks client metadata documents.
type metadataFetcher struct {
	client  *http.Client
	limiter *rate.Limiter
}

func newMetadataFetcher(cfg *DoorConfig) *metadataFetcher {
	timeout, burst := cfg.fetchTimeout, cfg.fetchBurst
	if timeout == 0 {
		timeout = metadataWait
	}
	if burst == 0 {
		burst = fetchBurst
	}
	dialer := &netguard.Dialer{Lookup: netguard.SystemLookup, Allow: netguard.PublicAddr}
	if cfg.fetchLookup != nil {
		dialer.Lookup = cfg.fetchLookup
	}
	if cfg.fetchAllow != nil {
		dialer.Allow = cfg.fetchAllow
	}
	dialer.Net.Timeout = timeout
	transport := &http.Transport{
		// No proxy from the environment: a proxy would connect where the guard cannot see.
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSClientConfig:        cfg.tlsConfig(),
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 16 << 10,
		DisableKeepAlives:      true,
	}
	return &metadataFetcher{
		limiter: rate.NewLimiter(fetchRate, burst),
		client: &http.Client{
			Transport: transport, Timeout: timeout,
			// A redirect is an answer from somewhere the door did not choose to ask.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *DoorConfig) tlsConfig() *tls.Config {
	if c.TLSConfig != nil {
		return c.TLSConfig.Clone()
	}
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// clientDocument is the part of a metadata document the door reads.
type clientDocument struct {
	ClientID      string   `json:"client_id"`
	ClientName    string   `json:"client_name"`
	RedirectURIs  []string `json:"redirect_uris"`
	AuthMethod    string   `json:"token_endpoint_auth_method"`
	ClientSecret  *string  `json:"client_secret"`
	JWKS          any      `json:"jwks"`
	JWKSURI       string   `json:"jwks_uri"`
	GrantTypesRaw []string `json:"grant_types"`
}

// confidential reports whether the document describes a client that holds a secret or a key, which
// the door cannot use: every app here is a public client that proves itself with PKCE.
func (c clientDocument) confidential() bool {
	if c.AuthMethod != "" && c.AuthMethod != authMethodNone {
		return true
	}
	return c.ClientSecret != nil || c.JWKS != nil || c.JWKSURI != ""
}

// validClientIDURL applies the document's rules to the address itself: https, a path, no
// credentials, fragment or dot segments.
func validClientIDURL(raw string) bool {
	if len(raw) > maxClientIDChars || strings.Contains(raw, "\\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != schemeHTTPS || u.Hostname() == "" {
		return false
	}
	return u.User == nil && u.Fragment == "" && u.Opaque == "" && pathAcceptable(u.Path)
}

// pathAcceptable requires a path of its own, with no dot segments.
func pathAcceptable(path string) bool {
	return path != "" && path != "/" && !hasDotSegment(path)
}

func hasDotSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// fetch gets the document at clientID and returns what it says, or why it cannot be used.
func (f *metadataFetcher) fetch(ctx context.Context, clientID string) (clientDocument, error) {
	if !validClientIDURL(clientID) {
		return clientDocument{}, errBadClientID
	}
	if !f.limiter.Allow() {
		return clientDocument{}, errFetchBusy
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return clientDocument{}, errBadClientID
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return clientDocument{}, errors.New("the client metadata could not be fetched")
	}
	defer func() { _ = resp.Body.Close() }()
	return readDocument(resp)
}

// readDocument reads a 200 JSON answer of bounded size.
func readDocument(resp *http.Response) (clientDocument, error) {
	if resp.StatusCode != http.StatusOK {
		return clientDocument{}, errBadMetadata
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return clientDocument{}, errBadMetadata
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil || len(raw) > maxMetadataBytes {
		return clientDocument{}, errBadMetadata
	}
	var doc clientDocument
	if json.Unmarshal(raw, &doc) != nil {
		return clientDocument{}, errBadMetadata
	}
	return doc, nil
}

// clientFromDocument checks what a document says about itself and turns it into a client. The
// document must name the address it was fetched from, must not be a confidential client (the
// door holds no secret for it), and every redirect address must pass the door's own rule.
func (d *door) clientFromDocument(clientID string, doc clientDocument) (clientInfo, error) {
	count := len(doc.RedirectURIs)
	if doc.ClientID != clientID || count == 0 || count > maxRedirectURIs || doc.confidential() {
		return clientInfo{}, errBadMetadata
	}
	for _, raw := range doc.RedirectURIs {
		if !d.base.redirectAllowed(raw) {
			return clientInfo{}, errBadMetadata
		}
	}
	name := truncate(doc.ClientName, maxDisplayLength)
	return clientInfo{ID: clientID, Name: name, RedirectURIs: doc.RedirectURIs}, nil
}

// resolveClient finds the chat app a client_id names: an https address is a metadata document the
// door fetches, anything else is a client registered by dynamic registration, whose id is the
// sealed registration.
func (d *door) resolveClient(ctx context.Context, clientID string) (clientInfo, error) {
	if strings.HasPrefix(clientID, "https://") {
		doc, err := d.fetcher.fetch(ctx, clientID)
		if err != nil {
			d.event("client metadata refused", "reason", err.Error())
			return clientInfo{}, errUnknownClient
		}
		info, err := d.clientFromDocument(clientID, doc)
		if err != nil {
			d.event("client metadata refused", "reason", err.Error())
			return clientInfo{}, errUnknownClient
		}
		return info, nil
	}
	var reg registration
	if clientID == "" || d.sealer.Open(purposeClient, clientID, &reg) != nil {
		return clientInfo{}, errUnknownClient
	}
	return clientInfo{ID: clientID, Name: reg.Name, RedirectURIs: reg.RedirectURIs}, nil
}
