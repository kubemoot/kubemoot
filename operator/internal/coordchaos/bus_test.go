/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// startNATS runs a real nats-server with JetStream and returns its URL. The test is
// skipped when the binary is not installed; the agent-runtime CI job installs the
// server and the operator job does not, so this runs where the server exists.
func startNATS(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("nats-server")
	if err != nil {
		t.Skip("nats-server is not installed")
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-js", "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-sd", t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		nc, err := nats.Connect(url)
		if err == nil {
			nc.Close()
			return url
		}
		if time.Now().After(deadline) {
			t.Fatalf("nats-server did not start: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func provision(t *testing.T, url string) (jetstream.JetStream, *nats.Conn) {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, subj := range map[string]string{streamCoordinator: "kubemoot.coordinator.>", streamDiscuss: "kubemoot.discuss.>"} {
		if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: name, Subjects: []string{subj}}); err != nil {
			t.Fatal(err)
		}
	}
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: defaultLeaseBucket})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(ctx, "hello.hello.thread-1", []byte("hello-coordinator-abc")); err != nil {
		t.Fatal(err)
	}
	return js, nc
}

func publish(t *testing.T, js jetstream.JetStream, subject, body string) {
	t.Helper()
	if _, err := js.Publish(context.Background(), subject, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

func TestNATSBusReadsBothStreamsAndCatchesUp(t *testing.T) {
	url := startNATS(t)
	js, _ := provision(t, url)
	since := time.Now().Add(-time.Minute)
	publish(t, js, "kubemoot.coordinator.hello.hello.thread-1",
		`{"v":1,"eventId":"thread-1/opened","type":"thread_opened","threadId":"thread-1","round":0,"data":{"conversationId":"c1","unknownField":1}}`)
	publish(t, js, "kubemoot.coordinator.hello.hello.thread-1",
		`{"v":1,"eventId":"thread-1/0/closed","type":"thread_closed","threadId":"thread-1","round":0,"data":{"reason":"synthesized"}}`)
	publish(t, js, "kubemoot.coordinator.other.crew.thread-9", `{"type":"thread_opened","threadId":"thread-9"}`)
	publish(t, js, "kubemoot.discuss.hello.hello.general.thread-1",
		`{"messageId":"m1","threadId":"thread-1","agentName":"c","messageType":"thread_start","metadata":{"conversationId":"c1"}}`)
	publish(t, js, "kubemoot.discuss.hello.hello.general.thread-1",
		`{"messageId":"m2","threadId":"thread-1","agentName":"c","messageType":"synthesis","content":"Paris"}`)
	publish(t, js, "kubemoot.discuss.hello.hello.general.thread-1", `not json`)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bus, err := DialBus(ctx, url, "hello", "hello", since)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := bus.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	ev, msgs := bus.Store().Counts()
	if ev != 2 || msgs != 2 {
		t.Fatalf("store holds %d events and %d messages, want 2 and 2", ev, msgs)
	}
	if ids := bus.Store().ThreadsFor("c1"); len(ids) != 1 || ids[0] != "thread-1" {
		t.Fatalf("threads for c1: %v", ids)
	}
	th := bus.Store().Thread("thread-1")
	if !th.Closed() || th.MsgCount(MsgSynthesis) != 1 {
		t.Fatalf("thread: %+v", th)
	}
}

func TestNATSBusSeesLaterWritesAfterCatchUp(t *testing.T) {
	url := startNATS(t)
	js, _ := provision(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bus, err := DialBus(ctx, url, "hello", "hello", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := bus.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	publish(t, js, "kubemoot.coordinator.hello.hello.thread-2",
		`{"v":1,"eventId":"thread-2/opened","type":"thread_opened","threadId":"thread-2","data":{"conversationId":"c2"}}`)
	if err := bus.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if !bus.Store().Thread("thread-2").Has(EvThreadOpened) {
		t.Fatal("a write made after the first catch-up was not read by the next one")
	}
}

func TestNATSBusLeaseHolder(t *testing.T) {
	url := startNATS(t)
	provision(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bus, err := DialBus(ctx, url, "hello", "hello", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if got, err := bus.LeaseHolder(ctx, "thread-1"); err != nil || got != "hello-coordinator-abc" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := bus.LeaseHolder(ctx, "no-such-thread"); err == nil {
		t.Fatal("a missing lease key returned no error")
	}
}
