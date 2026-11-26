package tokenstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

// natsStore is a store on an in-process NATS server with JetStream.
func natsStore(t *testing.T, maxTTL time.Duration) (*NATS, *natstest.Env) {
	t.Helper()
	env := natstest.Start(t, natstest.Options{JetStream: true})
	conn, err := env.Config.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	s, err := NewNATS(conn, "", maxTTL)
	if err != nil {
		t.Fatal(err)
	}
	return s, env
}

func TestNATSMeetsTheStoreContract(t *testing.T) {
	s, _ := natsStore(t, time.Hour)
	// The server's clock is the real one, so time passes by waiting.
	contract(t, s, time.Sleep)
	refusals(t, s)
}

func TestNATSValuesAreSharedAndSurviveAProcess(t *testing.T) {
	first, env := natsStore(t, time.Hour)
	ctx := context.Background()
	if err := first.Put(ctx, "account.example.0123abcd", []byte("sealed"), time.Hour); err != nil {
		t.Fatal(err)
	}
	// A second replica, with its own connection, reads what the first wrote.
	conn, err := env.Config.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	second, err := NewNATS(conn, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := second.Get(ctx, "account.example.0123abcd"); err != nil || string(got) != "sealed" {
		t.Fatalf("the second replica: %q %v", got, err)
	}
	// Exactly one of two replicas creates a key.
	errs := []error{
		first.Create(ctx, "code.one", []byte{1}, time.Minute),
		second.Create(ctx, "code.one", []byte{1}, time.Minute),
	}
	if errs[0] != nil || !errors.Is(errs[1], ErrExists) {
		t.Errorf("two creates of one key: %v", errs)
	}
	// A delete by one is seen by the other at once.
	if err := second.Delete(ctx, "account.example.0123abcd"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Get(ctx, "account.example.0123abcd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after the other replica's delete: %v", err)
	}
}

func TestNATSBucketExpiryIsTheLongestAValueLives(t *testing.T) {
	s, env := natsStore(t, 2*time.Second)
	ctx := context.Background()
	// The caller asks for an hour; the bucket allows two seconds.
	if err := s.Put(ctx, "greedy", []byte("v"), time.Hour); err != nil {
		t.Fatal(err)
	}
	conn, err := nats.Connect(env.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	js, _ := conn.JetStream()
	info, err := js.StreamInfo("KV_" + DefaultBucket)
	if err != nil || info.Config.MaxAge != 2*time.Second || info.Config.MaxMsgsPerSubject != 1 || !info.Config.AllowMsgTTL {
		t.Fatalf("bucket limits: %+v %v", info, err)
	}
	time.Sleep(3 * time.Second)
	if _, err := s.Get(ctx, "greedy"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a value outlived the bucket's expiry: %v", err)
	}
}

func TestNATSAwayIsAnErrorAndNeverNotFound(t *testing.T) {
	s, env := natsStore(t, time.Hour)
	ctx := context.Background()
	if err := s.Put(ctx, "kept", []byte("v"), time.Hour); err != nil {
		t.Fatal(err)
	}
	env.Server.Shutdown()
	env.Server.WaitForShutdown()
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := s.Get(short, "kept")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("get with the server away: %v", err)
	}
	if strings.Contains(err.Error(), "kept") {
		t.Errorf("the error names the key: %v", err)
	}
	if err := s.Put(short, "kept", []byte("v"), time.Hour); err == nil {
		t.Error("put with the server away succeeded")
	}
	if err := s.Create(short, "other", []byte("v"), time.Hour); err == nil || errors.Is(err, ErrExists) {
		t.Errorf("create with the server away: %v", err)
	}
	if err := s.Delete(short, "kept"); err == nil {
		t.Error("delete with the server away succeeded")
	}
}

// The bucket is made on first use. A first use that fails is not remembered, so a liaison
// that started while NATS could not be asked works on the next request.
func TestNATSAFailedFirstUseIsTriedAgain(t *testing.T) {
	env := natstest.Start(t, natstest.Options{JetStream: true})
	conn, err := env.Config.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s, err := NewNATS(conn, "late_bucket", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Put(gone, "k", []byte("v"), time.Hour); err == nil {
		t.Fatal("a put that could not reach the server succeeded")
	}
	if err := s.Put(context.Background(), "k", []byte("v"), time.Hour); err != nil {
		t.Fatalf("after the failure: %v", err)
	}
	if got, err := s.Get(context.Background(), "k"); err != nil || string(got) != "v" {
		t.Errorf("get: %q %v", got, err)
	}
}

func TestNewNATSRefusesNoLifetime(t *testing.T) {
	if _, err := NewNATS(nil, "", 0); !errors.Is(err, ErrBadTTL) {
		t.Errorf("no bucket expiry: %v", err)
	}
}

func TestWholeSecondsRoundsUp(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		time.Millisecond: time.Second, time.Second: time.Second, 1500 * time.Millisecond: 2 * time.Second,
		time.Hour: time.Hour, 30*24*time.Hour + time.Nanosecond: 30*24*time.Hour + time.Second,
	} {
		if got := wholeSeconds(in); got != want {
			t.Errorf("%v: %v, want %v", in, got, want)
		}
	}
}
