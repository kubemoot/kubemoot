/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// watchedSubscriber counts the subscribe attempts of the lifecycle server and notes the
// first one that succeeded, so a test waits on those states and never on a clock.
type watchedSubscriber struct {
	inner    subscriber
	attempts atomic.Int32
	// retried is closed at the third attempt: the server has failed twice and keeps trying.
	retried  chan struct{}
	ok       chan struct{}
	signaled atomic.Bool
}

func (w *watchedSubscriber) Subscribe(subject string, h nats.MsgHandler) (*nats.Subscription, error) {
	if w.attempts.Add(1) == 3 {
		close(w.retried)
	}
	sub, err := w.inner.Subscribe(subject, h)
	if err == nil && sub != nil && w.signaled.CompareAndSwap(false, true) {
		close(w.ok)
	}
	return sub, err
}

// natsGate stands at the address the operator dials. While it has no backend it closes
// every connection it accepts, which is what a client sees of a NATS that is down; once a
// backend is set it relays each connection to that NATS server. The test owns the listener
// from start to end, so the address is never free for another process to take.
type natsGate struct {
	listener net.Listener
	mu       sync.Mutex
	backend  string
}

// startNATSGate serves the listener as a gate with no backend and closes it with the test.
func startNATSGate(t *testing.T, l net.Listener) *natsGate {
	t.Helper()
	g := &natsGate{listener: l}
	t.Cleanup(func() { _ = l.Close() })
	go g.serve()
	return g
}

// url is the NATS URL of the gate.
func (g *natsGate) url() string { return "nats://" + g.listener.Addr().String() }

// open makes the gate relay to the NATS server at addr.
func (g *natsGate) open(addr string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.backend = addr
}

func (g *natsGate) serve() {
	for {
		c, err := g.listener.Accept()
		if err != nil {
			return // the listener closed with the test
		}
		go g.relay(c)
	}
}

// relay copies between the client and the backend until either side ends; with no backend
// it closes the client connection at once.
func (g *natsGate) relay(client net.Conn) {
	defer func() { _ = client.Close() }()
	g.mu.Lock()
	backend := g.backend
	g.mu.Unlock()
	if backend == "" {
		return
	}
	server, err := net.Dial("tcp", backend)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()
	go func() {
		_, _ = io.Copy(server, client)
		_ = server.Close()
	}()
	_, _ = io.Copy(client, server)
}

// startNATS runs an in-process NATS server on a port of its own and stops it with the test.
func startNATS(t *testing.T) *natsd.Server {
	t.Helper()
	s, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	t.Cleanup(s.Shutdown)
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("the in-process NATS server did not become ready")
	}
	return s
}

// waitForSubscription waits until the NATS server holds a subscription on the subject: a
// client's subscribe returns before the server has registered it.
func waitForSubscription(t *testing.T, s *natsd.Server, subject string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second) // safety net only
	for s.GlobalAccount().Interest(subject) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the NATS server never registered a subscription on %s", subject)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitFor waits for the signal and fails the test with what when it never comes.
func waitFor(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(30 * time.Second): // safety net only
		t.Fatal(what)
	}
}

// requestReady asks the lifecycle server, over the NATS server, to make the model ready.
func requestReady(t *testing.T, server *natsd.Server, model string) bus.ModelLifecycleReply {
	t.Helper()
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	msg, err := nc.Request(lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, model), 30*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var reply bus.ModelLifecycleReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

// With NATS down at its start, the lifecycle server keeps trying to subscribe, subscribes
// once NATS is up, and then answers a request to wake a model.
func TestTheLifecycleServerWakesAModelOnceNATSComesUpAfterAStartWithItDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := startNATSGate(t, l)
	adapter := asleepAwake()
	s, _ := lifecycleServer(t, adapter, namedServer(lifecycleServerName, aiv1alpha1.ServerTypeVLLM))
	watched := &watchedSubscriber{inner: kubemootnats.NewPublisher(gate.url()), retried: make(chan struct{}), ok: make(chan struct{})}
	s.Subscriber = watched
	s.Backoff = kubemootnats.Backoff{First: time.Millisecond, Max: 5 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = s.Start(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })

	waitFor(t, watched.retried, "the lifecycle server did not keep trying while NATS was down")
	if watched.signaled.Load() {
		t.Fatal("subscribed with NATS down")
	}
	server := startNATS(t)
	gate.open(server.Addr().String())
	waitFor(t, watched.ok, "the lifecycle server never subscribed after NATS came up")
	waitForSubscription(t, server, lifecycleSubject)

	reply := requestReady(t, server, vllmModel14)
	if !reply.Ok || reply.State != bus.ModelLifecycleReplyStateAwake {
		t.Fatalf("reply = %+v", reply)
	}
	adapter.mu.Lock()
	readied := len(adapter.readied)
	adapter.mu.Unlock()
	if readied != 1 {
		t.Errorf("the model was readied %d times, want once", readied)
	}
}
