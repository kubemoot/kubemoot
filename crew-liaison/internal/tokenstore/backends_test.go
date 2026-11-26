package tokenstore_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

func TestOpenBaoRefusals(t *testing.T) {
	tok := func() (string, error) { return "t", nil }
	cases := map[string]tokenstore.OpenBaoConfig{
		"plain http":  {Address: "http://bao.example.org", Token: tok},
		"not a url":   {Address: "::", Token: tok},
		"no host":     {Address: "https://", Token: tok},
		"no token":    {Address: "https://bao.example.org"},
		"unsupported": {Address: "ftp://bao.example.org", Token: tok, AllowInsecure: true},
	}
	for name, cfg := range cases {
		if _, err := tokenstore.NewOpenBao(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestOpenBaoNeverLeaksItsToken(t *testing.T) {
	_, srv := newFakeBao(t, "right-token")
	be, err := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{
		Address: srv.URL, AllowInsecure: true,
		Token: func() (string, error) { return "wrong-token-xyz", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Get(context.Background(), "g", "n")
	if err == nil || strings.Contains(err.Error(), "wrong-token-xyz") || errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("a refused token: %v", err)
	}
	empty, _ := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{Address: srv.URL, AllowInsecure: true,
		Token: func() (string, error) { return "", nil }})
	if _, err := empty.Get(context.Background(), "g", "n"); err == nil {
		t.Fatal("an empty token must fail before a request")
	}
	if _, err := be.Put(context.Background(), "g", "n", []byte("v"), 0, 0); !errors.Is(err, tokenstore.ErrBadTTL) {
		t.Fatalf("no ttl: %v", err)
	}
}

func TestOpenBaoUnreachable(t *testing.T) {
	_, srv := newFakeBao(t, "t")
	url := srv.URL
	srv.Close()
	be, _ := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{Address: url, AllowInsecure: true,
		Token: func() (string, error) { return "t", nil }})
	if _, err := be.Get(context.Background(), "g", "n"); err == nil || errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("a server that is away must be an error, not a miss: %v", err)
	}
}

func TestNATSReplicasShareValues(t *testing.T) {
	env := natstest.Start(t, natstest.Options{JetStream: true})
	open := func() tokenstore.Store {
		conn, err := env.Config.Connect()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conn.Close)
		be, err := tokenstore.NewNATS(conn, "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		s, err := tokenstore.NewSealed(be, testSealer(t, "k1"), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	first, second := open(), open()
	ctx := context.Background()
	if _, err := first.Put(ctx, rec("p", "a", "kept"), time.Hour, tokenstore.MustNotExist); err != nil {
		t.Fatal(err)
	}
	if got, err := second.Get(ctx, "p", "a"); err != nil || string(got.Value) != "kept" {
		t.Fatalf("the second replica: %v %v", got, err)
	}
	if _, err := second.Put(ctx, rec("p", "a", "again"), time.Hour, tokenstore.MustNotExist); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("the second replica creating the same record: %v", err)
	}
}

func TestNATSRefusals(t *testing.T) {
	if _, err := tokenstore.NewNATS(nil, "", 0); !errors.Is(err, tokenstore.ErrBadTTL) {
		t.Fatalf("no longest life: %v", err)
	}
}

func TestNATSDownIsAnErrorNotAMiss(t *testing.T) {
	env := natstest.Start(t, natstest.Options{JetStream: true})
	conn, err := env.Config.Connect()
	if err != nil {
		t.Fatal(err)
	}
	be, _ := tokenstore.NewNATS(conn, "", time.Hour)
	conn.Close()
	if _, err := be.Get(context.Background(), "g", "n"); err == nil || errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("a closed connection: %v", err)
	}
	if _, err := be.Put(context.Background(), "g", "n", []byte("v"), time.Hour, tokenstore.AnyRevision); err == nil {
		t.Fatal("put on a closed connection")
	}
}

func TestMemorySweep(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := tokenstore.NewMemory(func() time.Time { return now })
	ctx := context.Background()
	_, _ = m.Put(ctx, "g", "n", []byte("v"), time.Second, tokenstore.AnyRevision)
	if _, err := m.Put(ctx, "g", "n", []byte("v"), 0, tokenstore.AnyRevision); !errors.Is(err, tokenstore.ErrBadTTL) {
		t.Fatalf("no ttl: %v", err)
	}
	now = now.Add(2 * time.Second)
	m.Sweep()
	if names, _ := m.Names(ctx, "g"); len(names) != 0 {
		t.Fatalf("after the sweep: %v", names)
	}
}

func newBaoBackend(t *testing.T) (*tokenstore.OpenBao, *fakeBao) {
	t.Helper()
	fake, srv := newFakeBao(t, "tok")
	be, err := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{
		Address: srv.URL, AllowInsecure: true, Token: func() (string, error) { return "tok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return be, fake
}

func TestOpenBaoALosingWriterChangesNothing(t *testing.T) {
	be, fake := newBaoBackend(t)
	ctx := context.Background()
	rev, err := be.Put(ctx, "g", "n", []byte("one"), time.Hour, tokenstore.MustNotExist)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	sets, refusals := fake.ttlSets, fake.casRefusals
	fake.mu.Unlock()
	if _, err := be.Put(ctx, "g", "n", []byte("stale"), time.Minute, rev+5); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("a stale revision: %v", err)
	}
	if _, err := be.Put(ctx, "g", "n", []byte("again"), time.Minute, tokenstore.MustNotExist); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("create over a live secret: %v", err)
	}
	fake.mu.Lock()
	changed := fake.ttlSets != sets || fake.casRefusals != refusals
	fake.mu.Unlock()
	if changed {
		t.Fatal("a writer whose precondition fails must not touch the secret's time to live or write")
	}
	if got, _ := be.Get(ctx, "g", "n"); string(got.Value) != "one" || got.Revision != rev {
		t.Fatalf("the value changed: %+v", got)
	}
}

func TestOpenBaoAnExpiredSecretCanBeCreatedAgain(t *testing.T) {
	be, fake := newBaoBackend(t)
	ctx := context.Background()
	if _, err := be.Put(ctx, "g", "n", []byte("one"), 2*time.Second, tokenstore.MustNotExist); err != nil {
		t.Fatal(err)
	}
	fake.advance(5 * time.Second)
	if _, err := be.Get(ctx, "g", "n"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	// KV v2 keeps the deleted version's number, so the next write must carry it.
	rev, err := be.Put(ctx, "g", "n", []byte("two"), time.Hour, tokenstore.MustNotExist)
	if err != nil || rev < 2 {
		t.Fatalf("create after expiry: %d %v", rev, err)
	}
	if _, err := be.Put(ctx, "g", "n", []byte("three"), time.Hour, 1); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("a swap against the expired version: %v", err)
	}
}

func TestOpenBaoOnlyTheCheckAndSetErrorIsAConflict(t *testing.T) {
	var data int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/metadata/") && r.Method == http.MethodGet:
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/metadata/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			data++
			w.WriteHeader(http.StatusBadRequest)
			if data == 1 {
				_, _ = w.Write([]byte(`{"errors":["check-and-set parameter did not match the current version"]}`))
				return
			}
			_, _ = w.Write([]byte(`{"errors":["invalid path"],"note":"check-and-set parameter did not match the current version"}`))
		}
	}))
	defer srv.Close()
	be, _ := tokenstore.NewOpenBao(tokenstore.OpenBaoConfig{Address: srv.URL, AllowInsecure: true,
		Token: func() (string, error) { return "t", nil }})
	if _, err := be.Put(context.Background(), "g", "n", []byte("v"), time.Hour, tokenstore.MustNotExist); !errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("the check-and-set error: %v", err)
	}
	if _, err := be.Put(context.Background(), "g", "n", []byte("v"), time.Hour, tokenstore.MustNotExist); err == nil || errors.Is(err, tokenstore.ErrConflict) {
		t.Fatalf("another 400 must not read as a lost race: %v", err)
	}
}
