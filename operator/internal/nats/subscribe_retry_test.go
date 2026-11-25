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

package nats

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

var quick = Backoff{First: time.Millisecond, Max: 4 * time.Millisecond}

func TestSubscribeUntilReadyRetriesUntilNATSIsBack(t *testing.T) {
	want := &nats.Subscription{}
	calls := 0
	var waits []time.Duration
	sub, err := SubscribeUntilReady(context.Background(), func() (*nats.Subscription, error) {
		calls++
		if calls < 5 {
			return nil, errors.New("nats: no servers available for connection")
		}
		return want, nil
	}, quick, func(_ error, retryIn time.Duration) { waits = append(waits, retryIn) })
	if err != nil || sub != want {
		t.Fatalf("got %v, %v", sub, err)
	}
	if calls != 5 {
		t.Errorf("%d attempts, want 5", calls)
	}
	wantWaits := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}
	if len(waits) != len(wantWaits) {
		t.Fatalf("waits %v, want %v", waits, wantWaits)
	}
	for i := range waits {
		if waits[i] != wantWaits[i] {
			t.Errorf("wait %d is %v, want %v (doubling, capped at Max)", i, waits[i], wantWaits[i])
		}
	}
}

func TestSubscribeUntilReadyStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	done := make(chan struct{})
	var sub *nats.Subscription
	var err error
	go func() {
		sub, err = SubscribeUntilReady(ctx, func() (*nats.Subscription, error) {
			attempts++
			if attempts == 2 {
				cancel()
			}
			return nil, errors.New("down")
		}, quick, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SubscribeUntilReady did not return after the context ended")
	}
	if sub != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, %v; want nil and context.Canceled", sub, err)
	}
}

func TestSubscribeUntilReadyReturnsAtOnceWhenNATSIsNotConfigured(t *testing.T) {
	calls, failedCalls := 0, 0
	sub, err := SubscribeUntilReady(context.Background(), func() (*nats.Subscription, error) {
		calls++
		return nil, nil
	}, quick, func(error, time.Duration) { failedCalls++ })
	if sub != nil || err != nil || calls != 1 || failedCalls != 0 {
		t.Fatalf("got %v, %v after %d calls and %d failures; want nil, nil after one call", sub, err, calls, failedCalls)
	}
}

func TestSubscribeUntilReadySucceedsFirstTimeWithoutWaiting(t *testing.T) {
	want := &nats.Subscription{}
	failedCalls := 0
	sub, err := SubscribeUntilReady(context.Background(), func() (*nats.Subscription, error) { return want, nil },
		Backoff{First: time.Hour, Max: time.Hour}, func(error, time.Duration) { failedCalls++ })
	if err != nil || sub != want || failedCalls != 0 {
		t.Fatalf("got %v, %v, %d failures", sub, err, failedCalls)
	}
}

func TestAZeroBackoffUsesTheDefaultInsteadOfSpinning(t *testing.T) {
	if got := (Backoff{}).orDefault(); got != DefaultBackoff {
		t.Fatalf("zero backoff became %v, want %v", got, DefaultBackoff)
	}
	if got := (Backoff{First: time.Second, Max: time.Millisecond}).orDefault(); got.Max != time.Second {
		t.Fatalf("a Max below First became %v, want it raised to First", got.Max)
	}
}

func TestAPublisherKeepsAConnectionThatIsReconnecting(t *testing.T) {
	// A connection object that is not closed is kept (it reconnects on its own and replays
	// its subscriptions); no second one is dialed, which would leak the first.
	p := &Publisher{url: unreachableNATS}
	p.dialFn = func() (*nats.Conn, error) {
		t.Error("dialed although a connection that is not closed exists")
		return nil, errors.New("no dial expected")
	}
	conn := &nats.Conn{} // not connected and not closed: the state of a reconnecting connection
	p.conn = conn
	if err := p.lockConnected(); err != nil {
		t.Fatalf("lockConnected with a live connection: %v", err)
	}
	kept := p.conn
	p.mu.Unlock()
	if kept != conn {
		t.Fatal("a connection that was not closed was replaced")
	}
}

// unreachableNATS is an address nothing listens on, for tests of a NATS that is down.
const unreachableNATS = "nats://127.0.0.1:1"

func TestRetryUntilReadyReturnsTheValueOfTheFirstSuccessfulAttempt(t *testing.T) {
	calls := 0
	got, err := RetryUntilReady(context.Background(), func() (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("not ready")
		}
		return "the value", nil
	}, quick, nil)
	if err != nil || got != "the value" || calls != 3 {
		t.Fatalf("got %q, %v after %d calls", got, err, calls)
	}
}

func TestRetryUntilReadyStopsWithTheContextAndReturnsTheZeroValue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	got, err := RetryUntilReady(ctx, func() (int, error) {
		cancel()
		return 7, errors.New("down")
	}, quick, nil)
	if got != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %d, %v; want 0 and context.Canceled", got, err)
	}
}

func TestRetryUntilNoErrorReturnsNilOnceTheAttemptSucceeds(t *testing.T) {
	calls := 0
	err := RetryUntilNoError(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errors.New("not ready")
		}
		return nil
	}, quick, nil)
	if err != nil || calls != 3 {
		t.Fatalf("got %v after %d calls", err, calls)
	}
}

func TestADialInProgressDoesNotHoldThePublisherLock(t *testing.T) {
	calls := map[string]func(p *Publisher){
		"Subscribe": func(p *Publisher) { _, _ = p.Subscribe("kubemoot.test", func(*nats.Msg) {}) },
		"SubscribeDurable": func(p *Publisher) {
			_, _ = p.SubscribeDurable("S", "kubemoot.test", "d", func(*nats.Msg) {})
		},
		"BucketsReady":    func(p *Publisher) { _ = p.BucketsReady("b") },
		"Publish":         func(p *Publisher) { _ = p.Publish("kubemoot.test", "x") },
		"PublishOnChange": func(p *Publisher) { _ = p.PublishOnChange("kubemoot.test", "x") },
		"Connected":       func(p *Publisher) { _ = p.Connected() },
		"GetKVValue":      func(p *Publisher) { _, _ = p.GetKVValue("b", "k") },
		"CrewMemoryKeys":  func(p *Publisher) { _, _ = p.CrewMemoryKeys() },
		"GetObject":       func(p *Publisher) { _, _ = p.GetObject("b", "k") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) { assertDialLeavesTheLockFree(t, call) })
	}
}

// assertDialLeavesTheLockFree runs call against a server that accepts the connection and
// never speaks, so the dial waits for the INFO line the way a NATS that is half up does,
// and requires the publisher lock to be free while it waits.
func assertDialLeavesTheLockFree(t *testing.T, call func(p *Publisher)) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	p := NewPublisher("nats://" + l.Addr().String())
	done := make(chan struct{})
	go func() {
		call(p)
		close(done)
	}()
	var conn net.Conn
	select {
	case conn = <-accepted: // the dial is now waiting on the silent server
	case <-time.After(10 * time.Second): // safety net only
		t.Fatal("the call never dialed")
	}
	if !p.mu.TryLock() {
		t.Fatal("the call holds the publisher lock while it dials: publishes would stall for the dial timeout")
	}
	p.mu.Unlock()
	_ = conn.Close() // ends the dial with an error
	<-done
}

// startLocalNATS runs an in-process NATS server and returns its URL.
func startLocalNATS(t *testing.T) string {
	t.Helper()
	return startLocalServer(t, &natsd.Options{})
}

// startLocalJetStream runs an in-process NATS server with JetStream and returns its URL.
func startLocalJetStream(t *testing.T) string {
	t.Helper()
	return startLocalServer(t, &natsd.Options{JetStream: true, StoreDir: t.TempDir()})
}

// startLocalServer runs an in-process NATS server with opts on a loopback port of its own.
func startLocalServer(t *testing.T, opts *natsd.Options) string {
	t.Helper()
	opts.Host, opts.Port, opts.NoLog, opts.NoSigs = "127.0.0.1", -1, true, true
	srv, err := natsd.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("the in-process NATS server did not become ready")
	}
	return srv.ClientURL()
}

// gatedDial returns a dial that waits for release, then connects to url, and a channel that
// is closed when the dial has started.
func gatedDial(t *testing.T, url string) (dial func() (*nats.Conn, error), started chan struct{}, release chan struct{}, dialed chan *nats.Conn) {
	t.Helper()
	started, release, dialed = make(chan struct{}), make(chan struct{}), make(chan *nats.Conn, 1)
	dial = func() (*nats.Conn, error) {
		close(started)
		<-release
		c, err := nats.Connect(url)
		if err == nil {
			dialed <- c
		}
		return c, err
	}
	return dial, started, release, dialed
}

func TestAConnectionInstalledDuringADialWinsAndTheExtraOneIsClosed(t *testing.T) {
	url := startLocalNATS(t)
	dial, started, release, dialed := gatedDial(t, url)
	p := NewPublisher(url)
	p.dialFn = dial
	done := make(chan error, 1)
	go func() { done <- p.ensureConnected() }()
	<-started

	winner, err := nats.Connect(url) // another caller connects while the dial is in flight
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(winner.Close)
	p.mu.Lock()
	p.conn = winner
	p.mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	loser := <-dialed
	p.mu.Lock()
	kept := p.conn
	p.mu.Unlock()
	if kept != winner {
		t.Fatal("ensureConnected replaced a connection that appeared during its dial")
	}
	if !loser.IsClosed() {
		t.Fatal("the extra connection was not closed: it would leak")
	}
}

func TestCloseWaitsForADialInFlightAndLeavesNoConnectionBehind(t *testing.T) {
	url := startLocalNATS(t)
	dial, started, release, dialed := gatedDial(t, url)
	p := NewPublisher(url)
	p.dialFn = dial
	ensured := make(chan error, 1)
	go func() { ensured <- p.ensureConnected() }()
	<-started

	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	waitUntilCloseWaits(t, closed)
	close(release)
	if err := <-ensured; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(10 * time.Second): // safety net only
		t.Fatal("Close did not return once the dial finished")
	}
	conn := <-dialed
	p.mu.Lock()
	left := p.conn
	p.mu.Unlock()
	if left != nil {
		t.Fatal("Close left a connection installed")
	}
	waitUntilClosed(t, conn)
}

// waitUntilCloseWaits waits until the goroutine running Close is blocked taking a lock,
// which with a dial in flight is the dial lock. It fails when Close returns instead.
func waitUntilCloseWaits(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // safety net only
	for !goroutineBlockedOnLockIn("nats.(*Publisher).Close") {
		select {
		case <-closed:
			t.Fatal("Close returned while a dial was in flight")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Close neither returned nor waited for the dial")
		}
		time.Sleep(time.Millisecond)
	}
}

// goroutineBlockedOnLockIn reports whether some goroutine is blocked on a mutex with fn on
// its stack. It reads the wait reason the Go runtime prints in a goroutine's stack header
// ("sync.Mutex.Lock"), and does not tell which mutex of fn the goroutine waits for.
func goroutineBlockedOnLockIn(fn string) bool {
	buf := make([]byte, 1<<16)
	n := runtime.Stack(buf, true)
	for n == len(buf) { // the dump filled the buffer, so it may be cut short
		buf = make([]byte, 2*len(buf))
		n = runtime.Stack(buf, true)
	}
	for _, g := range strings.Split(string(buf[:n]), "\n\n") {
		header, _, _ := strings.Cut(g, "\n")
		if strings.Contains(header, "[sync.Mutex.Lock") && strings.Contains(g, fn) {
			return true
		}
	}
	return false
}

// waitUntilClosed waits for the connection to finish closing (a drain ends asynchronously).
func waitUntilClosed(t *testing.T, c *nats.Conn) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // safety net only
	for !c.IsClosed() {
		if time.Now().After(deadline) {
			t.Fatal("the connection was never closed")
		}
		time.Sleep(time.Millisecond)
	}
}
