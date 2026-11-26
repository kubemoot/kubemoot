package tokenstore_test

import (
	"context"
	"errors"
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
