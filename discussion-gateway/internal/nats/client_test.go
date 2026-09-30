package nats

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
)

func runServer(t *testing.T) *server.Server {
	t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server did not start")
	}
	return srv
}

// waitFor polls a condition the NATS library reaches on its own goroutines.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s never happened", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartConnectsAndReportsConnected(t *testing.T) {
	srv := runServer(t)
	defer srv.Shutdown()
	c := NewClient(srv.ClientURL())
	defer c.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if !c.Connected() {
		t.Fatal("want connected after Start")
	}
	if err := c.Start(); err != nil {
		t.Fatal("a second Start keeps the connection")
	}
	if _, err := c.JetStream(); err != nil {
		t.Fatalf("JetStream: %v", err)
	}
}

// The gateway may start before NATS: Start succeeds, readiness stays false, and the
// client connects once the server is there.
func TestStartBeforeTheServerConnectsWhenItArrives(t *testing.T) {
	srv := runServer(t)
	url := srv.ClientURL()
	port := srv.Addr().(*net.TCPAddr).Port
	srv.Shutdown()
	srv.WaitForShutdown()

	c := NewClient(url)
	defer c.Close()
	if err := c.Start(); err != nil {
		t.Fatalf("Start must not fail while NATS is down: %v", err)
	}
	if c.Connected() {
		t.Fatal("want not connected while NATS is down")
	}
	if _, err := c.JetStream(); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("JetStream while down: err = %v, want ErrNotConnected", err)
	}
	if err := c.Publish(context.Background(), "x", []byte("y")); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Publish while down: err = %v, want ErrNotConnected", err)
	}

	again, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	again.Start()
	defer again.Shutdown()
	waitFor(t, "connecting once NATS started", c.Connected)
}

func TestUnconfiguredClientIsANoOp(t *testing.T) {
	t.Setenv("NATS_URL", "")
	c := NewClient("")
	if c.IsConfigured() || c.URL() != "" {
		t.Fatal("want unconfigured")
	}
	if err := c.Start(); err != nil || c.Connected() {
		t.Fatalf("Start on an unconfigured client: %v, connected=%v", err, c.Connected())
	}
	if err := c.Publish(context.Background(), "x", nil); err != nil {
		t.Fatalf("Publish is a no-op without NATS: %v", err)
	}
	c.Close()
}

func TestNewClientFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv("NATS_URL", "nats://from-env:4222")
	if got := NewClient("").URL(); got != "nats://from-env:4222" {
		t.Fatalf("URL = %q", got)
	}
}

func TestPublishReachesJetStream(t *testing.T) {
	srv := runServer(t)
	defer srv.Shutdown()
	c := NewClient(srv.ClientURL())
	defer c.Close()
	js, err := c.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{Name: "S", Subjects: []string{"s.>"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Publish(context.Background(), "s.one", []byte("hi")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}
