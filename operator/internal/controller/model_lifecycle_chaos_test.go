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
	"errors"
	"fmt"
	"net"
	"runtime"
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

// freeAddr returns a loopback port nothing listens on yet.
func freeAddr(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("nats://127.0.0.1:%d", port), port
}

// startNATSOn runs an in-process NATS server on the port and stops it with the test.
func startNATSOn(t *testing.T, port int) {
	t.Helper()
	s, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: port, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	t.Cleanup(s.Shutdown)
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("the in-process NATS server did not become ready")
	}
}

// requestUntilAnswered sends the request until a responder exists: no responders means the
// server has not subscribed yet, any other outcome ends the wait.
func requestUntilAnswered(t *testing.T, nc *nats.Conn, subject string, body []byte) bus.ModelLifecycleReply {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second) // safety net only
	for {
		msg, err := nc.Request(subject, body, 10*time.Second)
		if err == nil {
			var reply bus.ModelLifecycleReply
			if err := json.Unmarshal(msg.Data, &reply); err != nil {
				t.Fatal(err)
			}
			return reply
		}
		if !errors.Is(err, nats.ErrNoResponders) || time.Now().After(deadline) {
			t.Fatalf("request: %v", err)
		}
		runtime.Gosched() // no responder yet: the subscription is still being registered
	}
}

// The failure that cost fitness arm A: the operator started while NATS was down, the
// lifecycle server subscribed once, and no agent could wake a model until a restart. It
// must subscribe by itself once NATS comes up.
func TestTheLifecycleServerWakesAModelOnceNATSComesUpAfterAStartWithItDown(t *testing.T) {
	url, port := freeAddr(t)
	adapter := asleepAwake()
	s, _ := lifecycleServer(t, adapter, namedServer(lifecycleServerName, aiv1alpha1.ServerTypeVLLM))
	watched := &watchedSubscriber{inner: kubemootnats.NewPublisher(url), retried: make(chan struct{}), ok: make(chan struct{})}
	s.Subscriber = watched
	s.Backoff = kubemootnats.Backoff{First: time.Millisecond, Max: 5 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = s.Start(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })

	select { // NATS is down: the server keeps trying
	case <-watched.retried:
	case <-time.After(30 * time.Second): // safety net only
		t.Fatal("the lifecycle server did not keep trying while NATS was down")
	}
	if watched.signaled.Load() {
		t.Fatal("subscribed with NATS down")
	}
	startNATSOn(t, port)
	select {
	case <-watched.ok:
	case <-time.After(30 * time.Second): // safety net only
		t.Fatal("the lifecycle server never subscribed after NATS came up")
	}

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	// Flushing the server's own connection first would need its handle; instead ask until a
	// responder exists, which is the state that matters.
	reply := requestUntilAnswered(t, nc, lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, vllmModel14))
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
