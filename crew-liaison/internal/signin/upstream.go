package signin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// What a call to the upstream's token endpoint can end in besides tokens.
var (
	// errUpstreamRefused means the upstream answered and said no: the code or the refresh
	// token is not valid (any more).
	errUpstreamRefused = errors.New("the sign-in service refused the request")
	// errUpstreamDown means the upstream could not be asked or gave no clear answer.
	errUpstreamDown = errors.New("the sign-in service could not be asked")
)

// upstreamTokens is the part of the upstream's token answer the front reads. The lifetime of
// the refresh token is not standard; the two names in use are both read.
type upstreamTokens struct {
	Access                string `json:"access_token"`
	Refresh               string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshExpiresIn      int64  `json:"refresh_expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

// refreshLifetime is how long the upstream says its refresh token lasts, in seconds, or 0.
func (t upstreamTokens) refreshLifetime() int64 {
	if t.RefreshExpiresIn > 0 {
		return t.RefreshExpiresIn
	}
	return max(t.RefreshTokenExpiresIn, 0)
}

// redeemCode exchanges the upstream's authorization code with the front's PKCE verifier.
func (f *front) redeemCode(ctx context.Context, code, verifier string) (upstreamTokens, error) {
	return f.callUpstream(ctx, url.Values{
		paramGrantType:  {grantAuthCode},
		paramCode:       {code},
		paramRedirect:   {f.cfg.PublicURL + pathCallback},
		paramClientID:   {f.cfg.UpstreamClientID},
		"code_verifier": {verifier},
	})
}

// redeemRefresh asks the upstream for new tokens with a refresh token.
func (f *front) redeemRefresh(ctx context.Context, refresh string) (upstreamTokens, error) {
	return f.callUpstream(ctx, url.Values{
		paramGrantType: {grantRefresh},
		grantRefresh:   {refresh},
		paramClientID:  {f.cfg.UpstreamClientID},
	})
}

// callUpstream posts form to the upstream token endpoint and reads the tokens it answers
// with. The body is never logged, and no redirect is followed. A 4xx is a refusal; anything
// else that is not tokens means the upstream could not be asked.
func (f *front) callUpstream(ctx context.Context, form url.Values) (upstreamTokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.UpstreamTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return upstreamTokens{}, errUpstreamDown
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		f.event("upstream token call failed")
		return upstreamTokens{}, errUpstreamDown
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstream))
	f.event("upstream token call", "grant", form.Get(paramGrantType), "status", resp.StatusCode)
	if err != nil {
		return upstreamTokens{}, errUpstreamDown
	}
	return readUpstreamTokens(resp.StatusCode, body)
}

func readUpstreamTokens(status int, body []byte) (upstreamTokens, error) {
	if status >= 400 && status < 500 {
		return upstreamTokens{}, errUpstreamRefused
	}
	var tokens upstreamTokens
	if status != http.StatusOK || json.Unmarshal(body, &tokens) != nil || tokens.Access == "" {
		return upstreamTokens{}, errUpstreamDown
	}
	return tokens, nil
}
