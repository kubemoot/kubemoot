package signin

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

func sharedReplayOver(t *testing.T, backend tokenstore.Backend) ReplaySet {
	t.Helper()
	sealer, err := ParseKeys(testKey(t, "replay"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := tokenstore.NewSealed(backend, sealer, 100*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewSharedReplay(store, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestSharedReplayRemembersUntilExpiry(t *testing.T) {
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	set := sharedReplayOver(t, tokenstore.NewMemory(func() time.Time { return clock }))
	if set.Remember("a", clock, clock.Add(time.Minute)) {
		t.Fatal("a first sight was reported as seen")
	}
	if !set.Remember("a", clock, clock.Add(time.Hour)) || !set.Has("a", clock) {
		t.Fatal("a second sight was not reported")
	}
	if set.Has("never", clock) {
		t.Error("an id never remembered is reported")
	}
	clock = clock.Add(2 * time.Minute)
	if set.Has("a", clock) || set.Remember("a", clock, clock.Add(time.Minute)) {
		t.Error("an expired id is still remembered")
	}
}

func TestSharedReplayNeedsAStore(t *testing.T) {
	if _, err := NewSharedReplay(nil, logr.Discard()); err == nil {
		t.Fatal("a nil store was accepted")
	}
}

// brokenBackend is a store that cannot answer.
type brokenBackend struct{ tokenstore.Backend }

func (brokenBackend) Get(context.Context, string, string) (tokenstore.Entry, error) {
	return tokenstore.Entry{}, errors.New("away")
}

func (brokenBackend) Put(context.Context, string, string, []byte, time.Duration, uint64) (uint64, error) {
	return 0, errors.New("away")
}

func TestSharedReplayRefusesWhenTheStoreIsAway(t *testing.T) {
	set := sharedReplayOver(t, brokenBackend{})
	now := time.Now()
	if !set.Remember("a", now, now.Add(time.Minute)) {
		t.Error("a doubt admitted a code")
	}
	if !set.Has("a", now) {
		t.Error("a doubt admitted a refresh token")
	}
}

func TestReplicasSharingAReplaySetRefuseEachOthersReuse(t *testing.T) {
	shared := sharedReplayOver(t, tokenstore.NewMemory(nil))
	a := newDoorRig(t, func(c *DoorConfig) { c.Replay = shared })
	b := newDoorRig(t, func(c *DoorConfig) {
		c.Replay, c.SealKeys, c.SigningKeys = shared, a.cfg.SealKeys, a.cfg.SigningKeys
	})
	_, refresh := a.tokens()
	if resp := a.postToken(a.refreshForm(refresh)); resp.StatusCode != http.StatusOK {
		t.Fatalf("the first use of a refresh token: %d", resp.StatusCode)
	}
	// The same token shown to the other replica is a copy.
	if resp := b.postToken(b.refreshForm(refresh)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a refresh token reused on another replica: %d", resp.StatusCode)
	}
}

func TestReplicasWithoutASharedSetDoNotSeeEachOther(t *testing.T) {
	a := newDoorRig(t, nil)
	b := newDoorRig(t, func(c *DoorConfig) { c.SealKeys, c.SigningKeys = a.cfg.SealKeys, a.cfg.SigningKeys })
	_, refresh := a.tokens()
	_ = a.postToken(a.refreshForm(refresh))
	if resp := b.postToken(b.refreshForm(refresh)); resp.StatusCode != http.StatusOK {
		t.Fatalf("the process-local memory is per replica: %d", resp.StatusCode)
	}
}
