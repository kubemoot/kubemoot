package tokenstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

// The OpenBao backend is exercised against a server that speaks KV version 2. The same checks
// run against the in-process fake on every test run, and against a real OpenBao (or Vault
// compatible) server when OPENBAO_ADDR is set, for example a dev server:
//
//	bao server -dev -dev-root-token-id=root &
//	OPENBAO_ADDR=http://127.0.0.1:8200 OPENBAO_TOKEN=root go test ./internal/tokenstore -run OpenBaoServer
//
// OPENBAO_MOUNT names the KV v2 mount (default "secret"). Each run writes under a prefix of its
// own and deletes what it wrote.
const (
	envBaoAddr  = "OPENBAO_ADDR"
	envBaoToken = "OPENBAO_TOKEN"
	envBaoMount = "OPENBAO_MOUNT"

	integGroup = "00112233445566778899aabbccddeeff"
	integName  = "ffeeddccbbaa99887766554433221100"
	// integLife is a time to live short enough to wait out and long enough that a slow server
	// does not expire the value between a write and the next read.
	integLife = 3 * time.Second
)

func realBao(t *testing.T) (*tokenstore.OpenBao, func(time.Duration)) {
	t.Helper()
	addr := os.Getenv(envBaoAddr)
	if addr == "" {
		t.Skip(envBaoAddr + " is not set: no OpenBao server to test against")
	}
	token := os.Getenv(envBaoToken)
	mount := os.Getenv(envBaoMount)
	be, err := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{
		Address: addr, Mount: mount, Prefix: fmt.Sprintf("kubemoot-it-%d", time.Now().UnixNano()),
		Token: func() (string, error) { return token, nil }, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return be, time.Sleep
}

func fakeBackend(t *testing.T) (*tokenstore.OpenBao, func(time.Duration)) {
	t.Helper()
	fake, srv := newFakeBao(t, "root-token")
	be, err := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{
		Address: srv.URL, Mount: "secret", Prefix: "it", AllowInsecure: true,
		Token: func() (string, error) { return "root-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return be, fake.advance
}

func TestOpenBaoServer(t *testing.T) {
	be, wait := realBao(t)
	runOpenBaoSuite(t, be, wait)
}

func TestOpenBaoSuiteAgainstTheFake(t *testing.T) {
	be, advance := fakeBackend(t)
	runOpenBaoSuite(t, be, advance)
}

// runOpenBaoSuite is what the OpenBao backend must do on any KV v2 server.
func runOpenBaoSuite(t *testing.T, be *tokenstore.OpenBao, wait func(time.Duration)) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() { _ = be.Delete(ctx, integGroup, integName) })
	rev1 := suiteCreate(t, ctx, be)
	suiteSwap(t, ctx, be, rev1)
	suiteListing(t, ctx, be)
	suiteDelete(t, ctx, be)
	suiteExpiry(t, ctx, be, wait)
}

// suiteCreate checks the compare-and-swap create: the first create wins, a second finds the key taken.
func suiteCreate(t *testing.T, ctx context.Context, be *tokenstore.OpenBao) uint64 {
	t.Helper()
	rev, err := be.Put(ctx, integGroup, integName, []byte("one"), time.Hour, tokenstore.MustNotExist)
	if err != nil || rev == 0 {
		t.Fatalf("create: %d %v", rev, err)
	}
	if _, err := be.Put(ctx, integGroup, integName, []byte("again"), time.Hour, tokenstore.MustNotExist); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("a second create must conflict: %v", err)
	}
	got, err := be.Get(ctx, integGroup, integName)
	if err != nil || string(got.Value) != "one" || got.Revision != rev {
		t.Fatalf("the losing create must change nothing: %+v %v", got, err)
	}
	return rev
}

// suiteSwap checks a swap at the current revision, and a conflict at a stale one.
func suiteSwap(t *testing.T, ctx context.Context, be *tokenstore.OpenBao, rev1 uint64) {
	t.Helper()
	rev2, err := be.Put(ctx, integGroup, integName, []byte("two"), time.Hour, rev1)
	if err != nil || rev2 <= rev1 {
		t.Fatalf("swap at the current revision: %d %v", rev2, err)
	}
	if _, err := be.Put(ctx, integGroup, integName, []byte("stale"), time.Hour, rev1); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("swap at a stale revision must conflict: %v", err)
	}
	got, err := be.Get(ctx, integGroup, integName)
	if err != nil || string(got.Value) != "two" || got.Revision != rev2 {
		t.Fatalf("after the conflict: %+v %v", got, err)
	}
	if _, err := be.Put(ctx, integGroup, integName, []byte("any"), time.Hour, tokenstore.AnyRevision); err != nil {
		t.Fatalf("overwrite at any revision: %v", err)
	}
}

func suiteListing(t *testing.T, ctx context.Context, be *tokenstore.OpenBao) {
	t.Helper()
	names, err := be.Names(ctx, integGroup)
	if err != nil || len(names) != 1 || names[0] != integName {
		t.Fatalf("names: %v %v", names, err)
	}
	groups, err := be.Groups(ctx)
	if err != nil || len(groups) != 1 || groups[0] != integGroup {
		t.Fatalf("groups: %v %v", groups, err)
	}
}

// suiteDelete checks that a delete is at once, repeatable, and leaves the key free to create.
func suiteDelete(t *testing.T, ctx context.Context, be *tokenstore.OpenBao) {
	t.Helper()
	if err := be.Delete(ctx, integGroup, integName); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Get(ctx, integGroup, integName); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := be.Delete(ctx, integGroup, integName); err != nil {
		t.Fatalf("deleting nothing: %v", err)
	}
	if names, err := be.Names(ctx, integGroup); err != nil || len(names) != 0 {
		t.Fatalf("names after delete: %v %v", names, err)
	}
}

// suiteExpiry checks that a version is gone when its time to live passes, and that the key can be
// created again then.
func suiteExpiry(t *testing.T, ctx context.Context, be *tokenstore.OpenBao, wait func(time.Duration)) {
	t.Helper()
	if _, err := be.Put(ctx, integGroup, integName, []byte("short"), integLife, tokenstore.MustNotExist); err != nil {
		t.Fatalf("create with a short life: %v", err)
	}
	if _, err := be.Get(ctx, integGroup, integName); err != nil {
		t.Fatalf("before the life ends: %v", err)
	}
	wait(integLife + 2*time.Second)
	if _, err := be.Get(ctx, integGroup, integName); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("after the life ends: %v", err)
	}
	if _, err := be.Put(ctx, integGroup, integName, []byte("fresh"), time.Hour, tokenstore.MustNotExist); err != nil {
		t.Fatalf("create after expiry: %v", err)
	}
}
