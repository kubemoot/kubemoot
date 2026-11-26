package tokenstore_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/signin"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

const (
	testMaxTTL = 3 * time.Second
	accLibrary = "library"
	pAlice     = "alice"
)

func testSealer(t testing.TB, ids ...string) *signin.Sealer {
	t.Helper()
	entries := make([]string, 0, len(ids))
	for _, id := range ids {
		key := make([]byte, 32)
		copy(key, id)
		entries = append(entries, id+":"+base64.StdEncoding.EncodeToString(key))
	}
	s, err := signin.ParseKeys(strings.Join(entries, ","))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// rig is one backend under test with the sealed store over it.
type rig struct {
	store   tokenstore.Store
	backend tokenstore.Backend
	// advance moves the backend's clock; the real ones wait.
	advance func(time.Duration)
}

type rigMaker func(t *testing.T) rig

func backends() map[string]rigMaker {
	return map[string]rigMaker{
		"memory":  memoryRig,
		"nats":    natsRig,
		"openbao": baoRig,
	}
}

func memoryRig(t *testing.T) rig {
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	mem := tokenstore.NewMemory(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	s, err := tokenstore.NewSealed(mem, testSealer(t, "k1"), testMaxTTL)
	if err != nil {
		t.Fatal(err)
	}
	return rig{store: s, backend: mem, advance: func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }}
}

func natsRig(t *testing.T) rig {
	env := natstest.Start(t, natstest.Options{JetStream: true})
	conn, err := env.Config.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	be, err := tokenstore.NewNATS(conn, "", testMaxTTL)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tokenstore.NewSealed(be, testSealer(t, "k1"), testMaxTTL)
	if err != nil {
		t.Fatal(err)
	}
	return rig{store: s, backend: be, advance: time.Sleep}
}

func baoRig(t *testing.T) rig {
	fake, srv := newFakeBao(t, "root-token")
	be, err := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{
		Address: srv.URL, Mount: "secret", Prefix: "liaison", AllowInsecure: true,
		Token: func() (string, error) { return "root-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := tokenstore.NewSealed(be, testSealer(t, "k1"), testMaxTTL)
	if err != nil {
		t.Fatal(err)
	}
	return rig{store: s, backend: be, advance: fake.advance}
}

func TestStoreContract(t *testing.T) {
	cases := map[string]func(*testing.T, rig){
		"round trip":                   contractRoundTrip,
		"compare and swap":             contractCAS,
		"one of many creates wins":     contractRaceCreate,
		"delete":                       contractDelete,
		"list and delete by principal": contractByPrincipal,
		"time to live":                 contractTTL,
		"time to live is capped":       contractTTLCap,
		"a copied value does not open": contractCopiedValue,
		"the backend holds ciphertext": contractCiphertext,
		"bad inputs are refused":       contractBadInputs,
	}
	for bname, maker := range backends() {
		for cname, run := range cases {
			t.Run(bname+"/"+cname, func(t *testing.T) { run(t, maker(t)) })
		}
	}
}

func rec(p, a, v string) tokenstore.Record {
	return tokenstore.Record{Principal: p, Account: a, Value: []byte(v)}
}

func contractRoundTrip(t *testing.T, r rig) {
	ctx := context.Background()
	rev, err := r.store.Put(ctx, rec("oidc:alice", accLibrary, "secret-1"), time.Hour, tokenstore.MustNotExist)
	if err != nil || rev == 0 {
		t.Fatalf("put: %d %v", rev, err)
	}
	got, err := r.store.Get(ctx, "oidc:alice", accLibrary)
	if err != nil || string(got.Value) != "secret-1" || got.Revision != rev || got.Principal != "oidc:alice" || got.Account != accLibrary {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := r.store.Get(ctx, "oidc:alice", "other"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("an account never written: %v", err)
	}
	if _, err := r.store.Get(ctx, "oidc:bob", accLibrary); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("another person's record: %v", err)
	}
}

func contractCAS(t *testing.T, r rig) {
	ctx := context.Background()
	rev1, _ := r.store.Put(ctx, rec("p", "a", "one"), time.Hour, tokenstore.MustNotExist)
	if _, err := r.store.Put(ctx, rec("p", "a", "again"), time.Hour, tokenstore.MustNotExist); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("create over an existing record: %v", err)
	}
	rev2, err := r.store.Put(ctx, rec("p", "a", "two"), time.Hour, rev1)
	if err != nil || rev2 == rev1 {
		t.Fatalf("swap at the current revision: %d %v", rev2, err)
	}
	if _, err := r.store.Put(ctx, rec("p", "a", "stale"), time.Hour, rev1); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("swap at a stale revision: %v", err)
	}
	if _, err := r.store.Put(ctx, rec("p", "a", "three"), time.Hour, tokenstore.AnyRevision); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ := r.store.Get(ctx, "p", "a")
	if string(got.Value) != "three" {
		t.Fatalf("value after the swaps: %q", got.Value)
	}
	if _, err := r.store.Put(ctx, rec("p", "never", "x"), time.Hour, 7); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("swap of a record that is not there: %v", err)
	}
}

func contractRaceCreate(t *testing.T, r rig) {
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.store.Put(context.Background(), rec("p", "a", "x"), time.Hour, tokenstore.MustNotExist); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d creates won, want exactly 1", wins.Load())
	}
}

func contractDelete(t *testing.T, r rig) {
	ctx := context.Background()
	_, _ = r.store.Put(ctx, rec("p", "a", "x"), time.Hour, tokenstore.AnyRevision)
	if err := r.store.Delete(ctx, "p", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Get(ctx, "p", "a"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := r.store.Delete(ctx, "p", "a"); err != nil {
		t.Fatalf("deleting nothing: %v", err)
	}
	// A deleted key can be created again.
	if _, err := r.store.Put(ctx, rec("p", "a", "y"), time.Hour, tokenstore.MustNotExist); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

func contractByPrincipal(t *testing.T, r rig) {
	ctx := context.Background()
	for _, x := range [][2]string{{pAlice, accLibrary}, {pAlice, "mail"}, {"bob", accLibrary}} {
		if _, err := r.store.Put(ctx, rec(x[0], x[1], "v-"+x[0]+x[1]), time.Hour, tokenstore.AnyRevision); err != nil {
			t.Fatal(err)
		}
	}
	list, err := r.store.ListByPrincipal(ctx, pAlice)
	if err != nil || len(list) != 2 {
		t.Fatalf("alice's records: %v %v", list, err)
	}
	for _, l := range list {
		if l.Principal != pAlice || string(l.Value) != "v-alice"+l.Account {
			t.Fatalf("a listed record: %+v", l)
		}
	}
	if none, err := r.store.ListByPrincipal(ctx, "carol"); err != nil || len(none) != 0 {
		t.Fatalf("a person with none: %v %v", none, err)
	}
	contractDeleteByPrincipal(t, r)
}

func contractDeleteByPrincipal(t *testing.T, r rig) {
	ctx := context.Background()
	n, err := r.store.DeleteByPrincipal(ctx, pAlice)
	if err != nil || n != 2 {
		t.Fatalf("delete by principal: %d %v", n, err)
	}
	if _, err := r.store.Get(ctx, pAlice, "mail"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("alice's record after sign-out: %v", err)
	}
	if _, err := r.store.Get(ctx, "bob", accLibrary); err != nil {
		t.Fatalf("bob's record must stay: %v", err)
	}
	if n, err := r.store.DeleteByPrincipal(ctx, pAlice); err != nil || n != 0 {
		t.Fatalf("deleting a person twice: %d %v", n, err)
	}
}

func contractTTL(t *testing.T, r rig) {
	ctx := context.Background()
	rev, _ := r.store.Put(ctx, rec("p", "a", "x"), 2*time.Second, tokenstore.MustNotExist)
	r.advance(1500 * time.Millisecond)
	// Using the record writes it again, which restarts its time to live.
	if _, err := r.store.Put(ctx, rec("p", "a", "x"), 2*time.Second, rev); err != nil {
		t.Fatal(err)
	}
	r.advance(1500 * time.Millisecond)
	if _, err := r.store.Get(ctx, "p", "a"); err != nil {
		t.Fatalf("a refreshed record 3s after the first write: %v", err)
	}
	r.advance(2500 * time.Millisecond)
	if _, err := r.store.Get(ctx, "p", "a"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("an unused record after its time to live: %v", err)
	}
	if list, _ := r.store.ListByPrincipal(ctx, "p"); len(list) != 0 {
		t.Fatalf("an expired record is still listed: %v", list)
	}
}

func contractTTLCap(t *testing.T, r rig) {
	ctx := context.Background()
	if _, err := r.store.Put(ctx, rec("p", "a", "x"), 1000*time.Hour, tokenstore.MustNotExist); err != nil {
		t.Fatal(err)
	}
	r.advance(testMaxTTL + 1500*time.Millisecond)
	if _, err := r.store.Get(ctx, "p", "a"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("a record asked to live 1000h outlives the store's longest life: %v", err)
	}
}

func contractCopiedValue(t *testing.T, r rig) {
	ctx := context.Background()
	_, _ = r.store.Put(ctx, rec(pAlice, accLibrary, "alice-token"), time.Hour, tokenstore.AnyRevision)
	group, name := tokenstore.KeyOf(pAlice, accLibrary)
	e, err := r.backend.Get(ctx, group, name)
	if err != nil {
		t.Fatal(err)
	}
	// Copy Alice's sealed value to Bob's key, and to another account of Alice's.
	bg, bn := tokenstore.KeyOf("bob", accLibrary)
	if _, err := r.backend.Put(ctx, bg, bn, e.Value, time.Hour, tokenstore.AnyRevision); err != nil {
		t.Fatal(err)
	}
	ag, an := tokenstore.KeyOf(pAlice, "mail")
	if _, err := r.backend.Put(ctx, ag, an, e.Value, time.Hour, tokenstore.AnyRevision); err != nil {
		t.Fatal(err)
	}
	copiedValuesDoNotOpen(t, r)
}

func copiedValuesDoNotOpen(t *testing.T, r rig) {
	ctx := context.Background()
	for _, who := range [][2]string{{"bob", accLibrary}, {pAlice, "mail"}} {
		got, err := r.store.Get(ctx, who[0], who[1])
		if err == nil || errors.Is(err, tokenstore.ErrNotFound) || strings.Contains(string(got.Value), "alice-token") {
			t.Fatalf("a copied value for %v opened: %+v %v", who, got, err)
		}
	}
	if list, _ := r.store.ListByPrincipal(ctx, "bob"); len(list) != 0 {
		t.Fatalf("a copied value is listed for bob: %v", list)
	}
	if list, _ := r.store.ListByPrincipal(ctx, pAlice); len(list) != 1 || list[0].Account != accLibrary {
		t.Fatalf("alice's list with a copy under another account: %v", list)
	}
}

func contractCiphertext(t *testing.T, r rig) {
	ctx := context.Background()
	_, _ = r.store.Put(ctx, rec("oidc:alice@example.org", accLibrary, "refresh-token-plaintext"), time.Hour, tokenstore.AnyRevision)
	group, name := tokenstore.KeyOf("oidc:alice@example.org", accLibrary)
	e, err := r.backend.Get(ctx, group, name)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"refresh-token-plaintext", pAlice, "example.org"} {
		if strings.Contains(string(e.Value), leak) || strings.Contains(group+name, leak) {
			t.Fatalf("the backend holds %q", leak)
		}
	}
}

func contractBadInputs(t *testing.T, r rig) {
	ctx := context.Background()
	if _, err := r.store.Put(ctx, rec("", "a", "x"), time.Hour, 0); !errors.Is(err, tokenstore.ErrBadRecord) {
		t.Fatalf("no principal: %v", err)
	}
	if _, err := r.store.Put(ctx, rec("p", "", "x"), time.Hour, 0); !errors.Is(err, tokenstore.ErrBadRecord) {
		t.Fatalf("no account: %v", err)
	}
	if _, err := r.store.Put(ctx, rec("p", "a", "x"), 0, 0); !errors.Is(err, tokenstore.ErrBadTTL) {
		t.Fatalf("no time to live: %v", err)
	}
	if _, err := r.store.Get(ctx, "", "a"); !errors.Is(err, tokenstore.ErrBadRecord) {
		t.Fatalf("get with no principal: %v", err)
	}
	if err := r.store.Delete(ctx, "p", ""); !errors.Is(err, tokenstore.ErrBadRecord) {
		t.Fatalf("delete with no account: %v", err)
	}
	if _, err := r.store.ListByPrincipal(ctx, ""); !errors.Is(err, tokenstore.ErrBadRecord) {
		t.Fatalf("list with no principal: %v", err)
	}
	if _, err := r.store.DeleteByPrincipal(ctx, ""); !errors.Is(err, tokenstore.ErrBadRecord) {
		t.Fatalf("delete by principal with none: %v", err)
	}
}

func TestSealedKeyRotation(t *testing.T) {
	ctx := context.Background()
	mem := tokenstore.NewMemory(nil)
	old, _ := tokenstore.NewSealed(mem, testSealer(t, "k1"), time.Hour)
	if _, err := old.Put(ctx, rec("p", "a", "v"), time.Minute, tokenstore.MustNotExist); err != nil {
		t.Fatal(err)
	}
	rotated, _ := tokenstore.NewSealed(mem, testSealer(t, "k2", "k1"), time.Hour)
	if got, err := rotated.Get(ctx, "p", "a"); err != nil || string(got.Value) != "v" {
		t.Fatalf("old key still opens: %v %v", got, err)
	}
	retired, _ := tokenstore.NewSealed(mem, testSealer(t, "k2"), time.Hour)
	if _, err := retired.Get(ctx, "p", "a"); err == nil || errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("a retired key must not open: %v", err)
	}
	if list, err := retired.ListByPrincipal(ctx, "p"); err != nil || len(list) != 0 {
		t.Fatalf("a record that does not open is left out of the list: %v %v", list, err)
	}
	if n, _ := retired.DeleteByPrincipal(ctx, "p"); n != 1 {
		t.Fatalf("a record that does not open must still be deletable: %d", n)
	}
}

func TestNewSealedRefusals(t *testing.T) {
	mem := tokenstore.NewMemory(nil)
	if _, err := tokenstore.NewSealed(nil, testSealer(t, "k"), time.Hour); err == nil {
		t.Fatal("no backend")
	}
	if _, err := tokenstore.NewSealed(mem, nil, time.Hour); err == nil {
		t.Fatal("no sealer")
	}
	if _, err := tokenstore.NewSealed(mem, testSealer(t, "k"), 0); !errors.Is(err, tokenstore.ErrBadTTL) {
		t.Fatalf("no longest life: %v", err)
	}
}
