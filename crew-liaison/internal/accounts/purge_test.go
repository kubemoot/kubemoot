package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

type failingDecls struct{ fakeDecls }

func (failingDecls) All(context.Context) ([]Declaration, error) { return nil, errors.New("api down") }

func newPurger(t *testing.T, decls Declarations) (*Purger, *Vault, *clock, *[]string) {
	t.Helper()
	v, c := newTestVault(t, nil)
	var logs []string
	log := funcr.New(func(prefix, args string) { logs = append(logs, prefix+args) }, funcr.Options{})
	return &Purger{Vault: v, Decls: decls, Now: c.now, Log: log}, v, c, &logs
}

func saveFor(t *testing.T, v *Vault, c *clock, principal, account string) {
	t.Helper()
	if _, err := v.Save(context.Background(), principal, account, token(c), tokenstore.AnyRevision); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeWaitsTheGraceThenDeletesAnUndeclaredAccount(t *testing.T) {
	decls := fakeDecls{acctLib: {Name: acctLib, Crews: []string{crewA}}}
	p, v, c, logs := newPurger(t, decls)
	saveFor(t, v, c, pAlice, acctLib)
	saveFor(t, v, c, pBob, acctLib)
	saveFor(t, v, c, pAlice, "mail")

	wantPurged(t, p, 0)
	c.advance(Grace - time.Hour)
	saveFor(t, v, c, pAlice, acctLib)
	wantPurged(t, p, 0)
	c.advance(2 * time.Hour)
	wantPurged(t, p, 1)
	ctx := context.Background()
	if _, _, err := v.Get(ctx, pAlice, "mail"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("the purged record: %v", err)
	}
	for _, who := range []string{pAlice, pBob} {
		if _, _, err := v.Get(ctx, who, acctLib); err != nil {
			t.Fatalf("a declared account must stay for %s: %v", who, err)
		}
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "mail") || strings.Contains((*logs)[0], pAlice) {
		t.Fatalf("the event names the account and no person: %v", *logs)
	}
}

func wantPurged(t *testing.T, p *Purger, want int) {
	t.Helper()
	if n, err := p.Run(context.Background()); err != nil || n != want {
		t.Fatalf("purged %d (want %d): %v", n, want, err)
	}
}

func TestPurgeClockRestartsWhenACrewDeclaresTheAccountAgain(t *testing.T) {
	decls := fakeDecls{}
	p, v, c, _ := newPurger(t, decls)
	ctx := context.Background()
	saveFor(t, v, c, pAlice, acctLib)
	if _, err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	c.advance(Grace - time.Hour)
	decls[acctLib] = Declaration{Name: acctLib, Crews: []string{crewA}}
	if n, err := p.Run(ctx); err != nil || n != 0 {
		t.Fatalf("declared again: %d %v", n, err)
	}
	delete(decls, acctLib)
	c.advance(2 * time.Hour)
	saveFor(t, v, c, pAlice, acctLib)
	if n, err := p.Run(ctx); err != nil || n != 0 {
		t.Fatalf("the old mark must not count after a redeclaration: %d %v", n, err)
	}
	if _, _, err := v.Get(ctx, pAlice, acctLib); err != nil {
		t.Fatalf("the record must stay: %v", err)
	}
}

func TestPurgeNeverActsWhenDeclarationsCannotBeRead(t *testing.T) {
	p, v, c, _ := newPurger(t, failingDecls{})
	ctx := context.Background()
	saveFor(t, v, c, pAlice, acctLib)
	c.advance(10 * Grace)
	saveFor(t, v, c, pAlice, acctLib)
	if n, err := p.Run(ctx); err == nil || n != 0 {
		t.Fatalf("a failed read must stop the pass: %d %v", n, err)
	}
	if _, _, err := v.Get(ctx, pAlice, acctLib); err != nil {
		t.Fatalf("nothing may be deleted on a guess: %v", err)
	}
}

func TestPurgeLeavesRefreshLeasesAlone(t *testing.T) {
	p, v, c, _ := newPurger(t, fakeDecls{})
	ctx := context.Background()
	if ok, err := v.TryLock(ctx, pAlice, acctLib, 3*Grace); err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if _, err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	c.advance(2 * Grace)
	if n, err := p.Run(ctx); err != nil || n != 0 {
		t.Fatalf("a lease is not a record: %d %v", n, err)
	}
}
