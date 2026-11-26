package accounts

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

const (
	crewGuide    = "guide"
	issuerURL    = "https://issuer.example.org"
	acctLib      = "lib"
	pAlice       = "alice"
	pBob         = "bob"
	accessR2     = "access-r2"
	testClientID = "cid"
	trueText     = "true"
)

// testSealer is AES-GCM under a fixed key with the purpose as additional data, like the
// liaison's real sealer, so a value moved to another purpose fails to open.
type testSealer struct{}

func (testSealer) aead() cipher.AEAD {
	block, _ := aes.NewCipher(make([]byte, 32))
	a, _ := cipher.NewGCM(block)
	return a
}

func (s testSealer) Seal(purpose string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	a := s.aead()
	nonce := make([]byte, a.NonceSize())
	return base64.RawURLEncoding.EncodeToString(a.Seal(nonce, nonce, plain, []byte(purpose))), nil
}

func (s testSealer) Open(purpose, blob string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(blob)
	a := s.aead()
	if err != nil || len(raw) < a.NonceSize() {
		return errors.New("bad")
	}
	plain, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(purpose))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestVault(t *testing.T, regs map[string]Registration) (*Vault, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)}
	store, err := tokenstore.NewSealed(tokenstore.NewMemory(c.now), testSealer{}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVault(store, regs, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return v, c
}

func token(c *clock) Token {
	return Token{Refresh: "r1", Access: "a1", AccessExpires: c.now().Add(time.Hour), GrantedAt: c.now(), LastUsed: c.now(), Crews: []string{"ns/crew"}}
}

func TestTimeToLiveIsTheIdleLifeCutAtTheServicesLimit(t *testing.T) {
	v, c := newTestVault(t, map[string]Registration{"capped": {MaxLifetime: 48 * time.Hour}})
	base := token(c)
	if got := v.TimeToLive(acctLib, base); got != IdleLife {
		t.Errorf("no limit: %v", got)
	}
	limited := base
	limited.KeepUntil = c.now().Add(5 * 24 * time.Hour)
	if got := v.TimeToLive(acctLib, limited); got != 5*24*time.Hour {
		t.Errorf("a service limit shorter than the idle life: %v", got)
	}
	far := base
	far.KeepUntil = c.now().Add(400 * 24 * time.Hour)
	if got := v.TimeToLive(acctLib, far); got != IdleLife {
		t.Errorf("a service limit longer than the idle life: %v", got)
	}
	if got := v.TimeToLive("capped", base); got != 48*time.Hour {
		t.Errorf("the install's per-account cap: %v", got)
	}
	noRefresh := Token{Access: "a", AccessExpires: c.now().Add(30 * time.Minute), GrantedAt: c.now()}
	if got := v.TimeToLive(acctLib, noRefresh); got != 30*time.Minute {
		t.Errorf("an access token alone is kept no longer than it lives: %v", got)
	}
	over := base
	over.KeepUntil = c.now().Add(-time.Second)
	if v.TimeToLive(acctLib, over) > 0 {
		t.Error("a limit in the past must leave nothing")
	}
}

func TestSaveGetAndSwap(t *testing.T) {
	v, c := newTestVault(t, nil)
	ctx := context.Background()
	rev, err := v.Save(ctx, "oidc:a", acctLib, token(c), tokenstore.MustNotExist)
	if err != nil {
		t.Fatal(err)
	}
	got, gotRev, err := v.Get(ctx, "oidc:a", acctLib)
	if err != nil || got.Refresh != "r1" || gotRev != rev {
		t.Fatalf("get: %+v %d %v", got, gotRev, err)
	}
	next := got
	next.Refresh = "r2"
	if _, err := v.Save(ctx, "oidc:a", acctLib, next, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Save(ctx, "oidc:a", acctLib, got, rev); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("a swap that lost the race: %v", err)
	}
	if _, _, err := v.Get(ctx, "oidc:b", acctLib); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("another person: %v", err)
	}
}

func TestAnExpiredTokenIsDeletedNotSaved(t *testing.T) {
	v, c := newTestVault(t, nil)
	ctx := context.Background()
	_, _ = v.Save(ctx, "p", acctLib, token(c), tokenstore.MustNotExist)
	over := token(c)
	over.KeepUntil = c.now().Add(-time.Minute)
	if _, err := v.Save(ctx, "p", acctLib, over, tokenstore.AnyRevision); !errors.Is(err, ErrExpired) {
		t.Fatalf("save of an over record: %v", err)
	}
	if _, _, err := v.Get(ctx, "p", acctLib); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("the record must be gone: %v", err)
	}
}

func TestUnusedRecordsGoAfterThirtyDaysAndUseRestartsTheClock(t *testing.T) {
	v, c := newTestVault(t, nil)
	ctx := context.Background()
	rev, _ := v.Save(ctx, "p", "used", token(c), tokenstore.MustNotExist)
	_, _ = v.Save(ctx, "p", "unused", token(c), tokenstore.MustNotExist)
	c.advance(20 * 24 * time.Hour)
	tok, _, _ := v.Get(ctx, "p", "used")
	v.Touch(ctx, "p", "used", tok, rev)
	c.advance(20 * 24 * time.Hour)
	if _, _, err := v.Get(ctx, "p", "used"); err != nil {
		t.Fatalf("a record used 20 days ago must stay: %v", err)
	}
	if _, _, err := v.Get(ctx, "p", "unused"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("a record unused for 40 days: %v", err)
	}
}

func TestTouchIsRateLimitedAndToleratesALostRace(t *testing.T) {
	v, c := newTestVault(t, nil)
	ctx := context.Background()
	rev, _ := v.Save(ctx, "p", acctLib, token(c), tokenstore.MustNotExist)
	tok, _, _ := v.Get(ctx, "p", acctLib)
	c.advance(time.Minute)
	v.Touch(ctx, "p", acctLib, tok, rev) // under an hour: no write
	_, again, _ := v.Get(ctx, "p", acctLib)
	if again != rev {
		t.Fatal("a touch within the hour wrote")
	}
	c.advance(2 * time.Hour)
	v.Touch(ctx, "p", acctLib, tok, rev+99) // stale revision: swallowed
	v.Touch(ctx, "p", acctLib, tok, rev)
	if _, after, _ := v.Get(ctx, "p", acctLib); after == rev {
		t.Fatal("a touch after the hour did not write")
	}
}

func TestDisconnectSignOutAndList(t *testing.T) {
	v, c := newTestVault(t, nil)
	ctx := context.Background()
	for _, a := range []string{"mail", acctLib} {
		_, _ = v.Save(ctx, pAlice, a, token(c), tokenstore.MustNotExist)
	}
	_, _ = v.Save(ctx, "bob", acctLib, token(c), tokenstore.MustNotExist)
	list, err := v.List(ctx, pAlice)
	if err != nil || len(list) != 2 || list[0].Account != acctLib || list[0].State != StateConnected {
		t.Fatalf("list: %+v %v", list, err)
	}
	disconnectAndSignOut(t, v, ctx)
}

func disconnectAndSignOut(t *testing.T, v *Vault, ctx context.Context) {
	t.Helper()
	if err := v.Disconnect(ctx, pAlice, acctLib); err != nil {
		t.Fatal(err)
	}
	if n, err := v.SignOut(ctx, pAlice); err != nil || n != 1 {
		t.Fatalf("sign out: %d %v", n, err)
	}
	if _, _, err := v.Get(ctx, "bob", acctLib); err != nil {
		t.Fatalf("bob's record: %v", err)
	}
	if list, _ := v.List(ctx, pAlice); len(list) != 0 {
		t.Fatalf("alice after sign-out: %+v", list)
	}
}

func TestNewVaultNeedsAStore(t *testing.T) {
	if _, err := NewVault(nil, nil, nil); err == nil {
		t.Fatal("no store")
	}
}

func TestConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	keys, secret, empty := write("keys", "k1:abc"), write("secret", "s3cret\n"), write("empty", " ")
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	offUnlessTrue(t, get, keys)
	ok := map[string]string{
		EnvEnabled: trueText, EnvSealKeysFile: keys,
		EnvRegistered: `{"library":{"clientId":"cid","clientSecretFile":"` + secret + `","allowedCrews":["ns/a"],"maxLifetimeHours":48}}`,
	}
	cfg, err := ConfigFromEnv(get(ok), nil)
	reg := cfg.Registrations["library"]
	if err != nil || !cfg.Enabled || reg.ClientID != testClientID || reg.ClientSecret != "s3cret" || reg.MaxLifetime != 48*time.Hour {
		t.Fatalf("on: %+v %v", cfg, err)
	}
	rejectsBadConfig(t, get, keys, empty, dir)
}

func offUnlessTrue(t *testing.T, get func(map[string]string) func(string) string, keys string) {
	t.Helper()
	if cfg, err := ConfigFromEnv(get(nil), nil); err != nil || cfg.Enabled {
		t.Fatalf("off by default: %+v %v", cfg, err)
	}
	if cfg, err := ConfigFromEnv(get(map[string]string{EnvEnabled: "false", EnvSealKeysFile: keys}), nil); err != nil || cfg.Enabled {
		t.Fatalf("explicitly off: %+v %v", cfg, err)
	}
}

func rejectsBadConfig(t *testing.T, get func(map[string]string) func(string) string, keys, empty, dir string) {
	t.Helper()
	bad := map[string]map[string]string{
		"no keys file":   {EnvEnabled: trueText},
		"empty keys":     {EnvEnabled: trueText, EnvSealKeysFile: empty},
		"missing keys":   {EnvEnabled: trueText, EnvSealKeysFile: filepath.Join(dir, "nope")},
		"not json":       {EnvEnabled: trueText, EnvSealKeysFile: keys, EnvRegistered: "[1]"},
		"bad name":       {EnvEnabled: trueText, EnvSealKeysFile: keys, EnvRegistered: `{"Bad_Name":{"clientId":"c"}}`},
		"no client id":   {EnvEnabled: trueText, EnvSealKeysFile: keys, EnvRegistered: `{"lib":{}}`},
		"negative life":  {EnvEnabled: trueText, EnvSealKeysFile: keys, EnvRegistered: `{"lib":{"clientId":"c","maxLifetimeHours":-1}}`},
		"missing secret": {EnvEnabled: trueText, EnvSealKeysFile: keys, EnvRegistered: `{"lib":{"clientId":"c","clientSecretFile":"` + filepath.Join(dir, "nope") + `"}}`},
	}
	for name, env := range bad {
		if _, err := ConfigFromEnv(get(env), nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestConfigErrorsCarryNoSecret(t *testing.T) {
	dir := t.TempDir()
	keys, empty := filepath.Join(dir, "keys"), filepath.Join(dir, "empty")
	_ = os.WriteFile(keys, []byte("k1:abc"), 0o600)
	_ = os.WriteFile(empty, []byte(" "), 0o600)
	env := map[string]string{
		EnvEnabled: trueText, EnvSealKeysFile: keys,
		EnvRegistered: `{"lib":{"clientId":"c","clientSecretFile":"` + empty + `"}}`,
	}
	_, err := ConfigFromEnv(func(k string) string { return env[k] }, nil)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("an empty secret file: %v", err)
	}
}

func TestRegistrationAllows(t *testing.T) {
	open := Registration{}
	if !open.Allows("ns/any") {
		t.Error("no list allows every crew")
	}
	r := Registration{AllowedCrews: []string{crewGuide, "other/special"}}
	for crew, want := range map[string]bool{"ns/guide": true, "other/special": true, "ns/special": false, "ns/else": false, "": false} {
		if r.Allows(crew) != want {
			t.Errorf("%q: want %v", crew, want)
		}
	}
}
