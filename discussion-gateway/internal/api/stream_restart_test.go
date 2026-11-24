package api

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
	"github.com/nats-io/nats-server/v2/server"
)

// restartableNATS runs a JetStream server on a fixed port and store, so the test can stop
// it and start it again at the same address with its streams intact.
type restartableNATS struct {
	t    *testing.T
	opts *server.Options
	srv  *server.Server
}

func newRestartableNATS(t *testing.T) *restartableNATS {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	r := &restartableNATS{t: t, opts: &server.Options{
		Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	}}
	r.start()
	t.Cleanup(func() { r.srv.Shutdown() })
	return r
}

func (r *restartableNATS) start() {
	r.t.Helper()
	srv, err := server.NewServer(r.opts)
	if err != nil {
		r.t.Fatal(err)
	}
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		r.t.Fatal("NATS server did not start")
	}
	r.srv = srv
}

func (r *restartableNATS) stop() {
	r.srv.Shutdown()
	r.srv.WaitForShutdown()
}

// A NATS restart while the answer is being produced must not end the stream: the stream
// waits for NATS, continues after the last message it handled, and delivers the answer
// published once NATS is back, then done. It never ends without a terminal event.
func TestStreamSurvivesANATSRestartBeforeTheAnswer(t *testing.T) {
	nats := newRestartableNATS(t)
	nc := natsclient.NewClient(nats.srv.ClientURL())
	if err := nc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	createStreams(t, js)
	since := time.Now().UTC().Format(time.RFC3339Nano)
	publishDiscuss(t, js, startOf("A"))
	publishDiscuss(t, js, msgOf("A", "a1", phaseTriaging, ""))

	srv := gatewayServer(t, nc)
	resp, err := http.Get(srv.URL + "/api/v1/discussions/pilot/conv-1/stream?since=" + url.QueryEscape(since))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := make(chan string, 64)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	before := waitForLine(t, lines, `"status":"triaging"`)

	nats.stop()
	nats.start()
	publishAfterRestart(t, nc, "A")

	all := strings.Join(before, "\n") + "\n" + readToTerminal(t, lines)
	if !strings.Contains(all, `"type":"synthesis","content":"the answer"`) || !strings.Contains(all, `"type":"done"`) {
		t.Fatalf("stream lacks the answer and done after the restart:\n%s", all)
	}
	assertOnceAndInOrder(t, all)
}

// assertOnceAndInOrder checks what the restart must not disturb: connected, the thread
// and the message handled before the restart appear once, the message that landed right
// after it arrives once, and the SSE ids only rise.
func assertOnceAndInOrder(t *testing.T, stream string) {
	t.Helper()
	for want, count := range map[string]int{
		`"type":"connected"`:    1,
		`"type":"thread_found"`: 1,
		`"status":"triaging"`:   1, // a1, handled before the restart
		`"status":"evaluating"`: 1, // a2, landed as NATS came back
		`"type":"synthesis"`:    1,
		`"type":"done"`:         1,
	} {
		if got := strings.Count(stream, want); got != count {
			t.Errorf("%s appears %d times, want %d:\n%s", want, got, count, stream)
		}
	}
	var last uint64
	for _, l := range strings.Split(stream, "\n") {
		id, ok := strings.CutPrefix(l, "id: ")
		if !ok {
			continue
		}
		seq, err := strconv.ParseUint(id[strings.LastIndex(id, ":")+1:], 10, 64)
		if err != nil || seq <= last {
			t.Errorf("event id %q does not rise above %d", id, last)
		}
		last = seq
	}
}

// readToTerminal collects stream lines up to a done or error event; the stream ending
// first, or no terminal event in time, fails the test.
func readToTerminal(t *testing.T, lines <-chan string) string {
	t.Helper()
	var got []string
	deadline := time.After(30 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended without a terminal event; saw %v", got)
			}
			got = append(got, l)
			if strings.Contains(l, `"type":"done"`) || strings.Contains(l, `"type":"error"`) {
				return strings.Join(got, "\n")
			}
		case <-deadline:
			t.Fatalf("no terminal event; saw %v", got)
		}
	}
}

// publishAfterRestart publishes the answer and close of thread once the restarted NATS
// accepts a publish.
func publishAfterRestart(t *testing.T, nc *natsclient.Client, thread string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := publishAnswer(nc, thread)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("NATS did not accept a publish after the restart: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// publishAnswer publishes the evaluating, synthesis and close messages of thread, in order.
func publishAnswer(nc *natsclient.Client, thread string) error {
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	subject := "kubemoot.discuss.ns.pilot.broadcast." + thread
	for _, m := range []string{
		`{"messageId":"a2","messageType":"evaluating","threadId":"` + thread + `","agentName":"agent-b"}`,
		`{"messageId":"s1","messageType":"synthesis","threadId":"` + thread + `","content":"the answer"}`,
		`{"messageId":"c1","messageType":"thread_close","threadId":"` + thread + `"}`,
	} {
		if _, err = js.Publish(context.Background(), subject, []byte(m)); err != nil {
			return err
		}
	}
	return nil
}

func waitForLine(t *testing.T, lines <-chan string, want string) []string {
	t.Helper()
	var seen []string
	timeout := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %s", want)
			}
			seen = append(seen, l)
			if strings.Contains(l, want) {
				return seen
			}
		case <-timeout:
			t.Fatalf("no line with %s", want)
		}
	}
}

// When NATS does not come back within the bound, the stream ends with an error event,
// never silently.
func TestStreamReportsAnErrorWhenNATSDoesNotComeBack(t *testing.T) {
	oldBound, oldBeat := resumeBound, pullHeartbeat
	resumeBound, pullHeartbeat = 1500*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { resumeBound, pullHeartbeat = oldBound, oldBeat })

	nats := newRestartableNATS(t)
	nc := natsclient.NewClient(nats.srv.ClientURL())
	if err := nc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, _ := nc.JetStream()
	createStreams(t, js)
	srv := gatewayServer(t, nc)
	resp, err := http.Get(srv.URL + "/api/v1/discussions/pilot/conv-1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Scan()
	for !strings.Contains(sc.Text(), `"type":"connected"`) && sc.Scan() {
	}
	nats.stop()
	var all []string
	for sc.Scan() {
		all = append(all, sc.Text())
	}
	if !strings.Contains(strings.Join(all, "\n"), `"type":"error"`) {
		t.Fatalf("stream ended without an error event: %v", all)
	}
}
