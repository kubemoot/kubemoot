package signin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// accessLifetime is how long a door access token is good for. The app renews it with its
// refresh token.
const accessLifetime = 15 * time.Minute

// doorToken is the access token the front issues: sealed, so only this liaison reads it.
// It names the door as issuer and audience, and Resolve refuses one issued for another door
// even when both doors share sealing keys.
type doorToken struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Subject  string `json:"sub"`
	Session  string `json:"sid"`
	ClientID string `json:"cid"`
	ID       string `json:"jti"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

// refreshGrant is the door's refresh token: it names a session and the one refresh id that
// session currently accepts. Its lifetime is the session record's.
type refreshGrant struct {
	Session  string `json:"sid"`
	ID       string `json:"jti"`
	Subject  string `json:"sub"`
	ClientID string `json:"cid"`
}

// session is one app's sign-in at the door, kept in the store. Refresh is the id of the only
// refresh token it accepts; each use replaces it, so a refresh token works once.
type session struct {
	Subject  string `json:"sub"`
	ClientID string `json:"cid"`
	Refresh  string `json:"rid"`
}

func sessionKey(id string) string { return "session." + id }

// tokenResponse is the answer of the token endpoint (RFC 6749 section 5.1).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// issue writes the session with a fresh refresh id and returns the door's token pair for it.
func (f *front) issue(ctx context.Context, sessionID string, s session) (tokenResponse, error) {
	refreshID, err := randomString(16)
	if err != nil {
		return tokenResponse{}, err
	}
	accessID, err := randomString(16)
	if err != nil {
		return tokenResponse{}, err
	}
	s.Refresh = refreshID
	if err := f.putSealed(ctx, sessionKey(sessionID), s, idleLifetime); err != nil {
		return tokenResponse{}, err
	}
	now := f.now()
	access, err := f.sealer.Seal(purposeDoor, doorToken{
		Issuer: f.cfg.PublicURL, Audience: f.doorResource(), Subject: s.Subject, Session: sessionID,
		ClientID: s.ClientID, ID: accessID, IssuedAt: now.Unix(), Expires: now.Add(accessLifetime).Unix(),
	})
	if err != nil {
		return tokenResponse{}, err
	}
	refresh, err := f.sealer.Seal(purposeRefresh, refreshGrant{
		Session: sessionID, ID: refreshID, Subject: s.Subject, ClientID: s.ClientID,
	})
	if err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{
		AccessToken: access, TokenType: "Bearer", ExpiresIn: int64(accessLifetime.Seconds()), RefreshToken: refresh,
	}, nil
}

// loadSession returns the session, or tokenstore.ErrNotFound when it ended.
func (f *front) loadSession(ctx context.Context, id string) (session, error) {
	var s session
	err := f.getSealed(ctx, sessionKey(id), &s)
	if errors.Is(err, errBadBlob) {
		return session{}, tokenstore.ErrNotFound
	}
	return s, err
}

// endSession deletes one app's sign-in at the door.
func (f *front) endSession(ctx context.Context, id string) {
	if err := f.store.Delete(ctx, sessionKey(id)); err != nil {
		f.event("could not end a door session", "error", err.Error())
	}
}

// Resolve implements SignIn.
func (f *front) Resolve(r *http.Request, bearer string) (identity.Caller, string, error) {
	var tok doorToken
	if f.sealer.Open(purposeDoor, bearer, &tok) != nil {
		return identity.Caller{}, "", identity.ErrNoCredential
	}
	if !f.current(tok) {
		f.event("door token refused", "reason", "expired or issued for another door")
		return identity.Caller{}, "", identity.ErrInvalid
	}
	return f.personOf(r, tok.Subject)
}

// current reports whether tok was issued by this door, for this door, and has time left.
func (f *front) current(tok doorToken) bool {
	return tok.Issuer == f.cfg.PublicURL && tok.Audience == f.doorResource() &&
		tok.Subject != "" && f.now().Unix() <= tok.Expires
}

// personOf returns the person subject names and the upstream token kept for them. The kept
// token is renewed when it is due, and once more when the upstream stops accepting it early.
// It must still name the same person, so a record can never serve anyone else.
func (f *front) personOf(r *http.Request, subject string) (identity.Caller, string, error) {
	ctx := r.Context()
	acct, err := f.loadAccount(ctx, subject)
	if err == nil && f.due(acct) {
		acct, err = f.renewAccount(ctx, subject, acct)
	}
	if err != nil {
		return identity.Caller{}, "", err
	}
	caller, err := f.cfg.Identify(r, acct.Access)
	if errors.Is(err, identity.ErrInvalid) {
		if acct, err = f.renewAccount(ctx, subject, acct); err == nil {
			caller, err = f.cfg.Identify(r, acct.Access)
		}
	}
	if err != nil {
		return identity.Caller{}, "", f.endOnRefusal(ctx, subject, err)
	}
	if caller.Principal.ID != subject {
		f.event("door token refused", "reason", "the kept token names another person")
		return identity.Caller{}, "", identity.ErrInvalid
	}
	f.touch(ctx, subject, acct)
	return caller, acct.Access, nil
}

// endOnRefusal deletes the kept token when the upstream no longer accepts it, and passes
// err on. A check that could not be made deletes nothing.
func (f *front) endOnRefusal(ctx context.Context, subject string, err error) error {
	if errors.Is(err, identity.ErrInvalid) {
		f.forgetAccount(ctx, subject, "the upstream no longer accepts it")
	}
	return err
}
