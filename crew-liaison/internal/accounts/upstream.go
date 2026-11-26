package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// What a call to a service's endpoint can end in besides tokens.
var (
	// ErrRefused means the service answered and said no: the code or the refresh token is not
	// valid (any more).
	ErrRefused = errors.New("the service refused the request")
	// ErrUnavailable means the service could not be asked or gave no clear answer.
	ErrUnavailable = errors.New("the service could not be asked")
)

const (
	maxServiceReply = 64 << 10
	serviceWait     = 15 * time.Second
	grantCode       = "authorization_code"
	grantRefresh    = "refresh_token"
)

// Tokens is the part of a service's token answer the liaison reads. The lifetime of a
// refresh token is not standard; the two names in use are both read.
type Tokens struct {
	Access                string `json:"access_token"`
	Refresh               string `json:"refresh_token"`
	Scope                 string `json:"scope"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshExpiresIn      int64  `json:"refresh_expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

// RefreshLifetime is how long the service says its refresh token lasts, or zero.
func (t Tokens) RefreshLifetime() time.Duration {
	secs := t.RefreshExpiresIn
	if secs <= 0 {
		secs = t.RefreshTokenExpiresIn
	}
	return time.Duration(max(secs, 0)) * time.Second
}

// Client calls a service's token and revocation endpoints. It follows no redirect, reads a
// bounded answer, and never logs or returns a token or a body in an error.
type Client struct {
	http *http.Client
	// allowHTTP lets tests use a plain http server; the chart never sets it.
	allowHTTP bool
}

// NewClient returns a Client. httpClient nil is a client with a timeout.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: serviceWait}
	}
	c := *httpClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &c}
}

// Exchange trades an authorization code (with the PKCE verifier) for tokens.
func (c *Client) Exchange(
	ctx context.Context, d Declaration, reg Registration, code, verifier, redirect string,
) (Tokens, error) {
	return c.token(ctx, d.TokenURL, reg, url.Values{
		"grant_type": {grantCode}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier},
	})
}

// Refresh trades a refresh token for new tokens.
func (c *Client) Refresh(ctx context.Context, d Declaration, reg Registration, refresh string) (Tokens, error) {
	return c.token(ctx, d.TokenURL, reg, url.Values{"grant_type": {grantRefresh}, "refresh_token": {refresh}})
}

func (c *Client) token(ctx context.Context, endpoint string, reg Registration, form url.Values) (Tokens, error) {
	status, body, err := c.post(ctx, endpoint, reg, form)
	if err != nil {
		return Tokens{}, err
	}
	if status >= 400 && status < 500 {
		return Tokens{}, ErrRefused
	}
	var t Tokens
	if status != http.StatusOK || json.Unmarshal(body, &t) != nil || t.Access == "" {
		return Tokens{}, ErrUnavailable
	}
	return t, nil
}

// Revoke asks the service to revoke token (RFC 7009). A service that does not answer 200 is
// an error the caller reports but does not wait on: the record is deleted either way.
func (c *Client) Revoke(ctx context.Context, d Declaration, reg Registration, token, hint string) error {
	if d.RevocationURL == "" {
		return errors.New("the service has no revocation endpoint")
	}
	status, _, err := c.post(ctx, d.RevocationURL, reg, url.Values{"token": {token}, "token_type_hint": {hint}})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return ErrUnavailable
	}
	return nil
}

// endpointOK reports whether the endpoint is an https address with a host (http only in tests).
func (c *Client) endpointOK(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (c.allowHTTP && u.Scheme == "http")
}

// post sends a form with the install's client credentials (basic when there is a secret,
// else the client id in the form).
func (c *Client) post(ctx context.Context, endpoint string, reg Registration, form url.Values) (int, []byte, error) {
	if !c.endpointOK(endpoint) {
		return 0, nil, ErrUnavailable
	}
	if reg.ClientSecret == "" {
		form.Set("client_id", reg.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if reg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(reg.ClientID), url.QueryEscape(reg.ClientSecret))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxServiceReply))
	if err != nil {
		return 0, nil, ErrUnavailable
	}
	return resp.StatusCode, body, nil
}
