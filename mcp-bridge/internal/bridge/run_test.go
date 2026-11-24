package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// safetyDeadline bounds every wait below. A wait ends as soon as its state is
// reached; the deadline only stops a broken test from hanging.
const safetyDeadline = 15 * time.Second

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(safetyDeadline)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// fakeServer plays an MCP server on the bridge's FIFOs: it answers every request
// that carries an id with the same id and a result, and records what it was sent.
type fakeServer struct {
	stdin, stdout *os.File
	lines         chan string
	done          chan struct{}
}

func startFakeServer(t *testing.T, dir string) *fakeServer {
	t.Helper()
	// Opening blocks until the bridge opens the other end of each pipe.
	stdin, err := os.OpenFile(filepath.Join(dir, "stdin"), os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := os.OpenFile(filepath.Join(dir, "stdout"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{stdin: stdin, stdout: stdout, lines: make(chan string, 32), done: make(chan struct{})}
	go f.serve()
	return f
}

func (f *fakeServer) serve() {
	defer close(f.done)
	sc := bufio.NewScanner(f.stdin)
	for sc.Scan() {
		line := sc.Text()
		f.lines <- line
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal([]byte(line), &rpc) != nil || len(rpc.ID) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(f.stdout, `{"jsonrpc":"2.0","id":%s,"result":{"echo":%q}}`+"\n", rpc.ID, rpc.Method)
	}
}

// nextLine returns the next line the server received.
func (f *fakeServer) nextLine(t *testing.T) string {
	t.Helper()
	select {
	case l := <-f.lines:
		return l
	case <-time.After(safetyDeadline):
		t.Fatal("the server received nothing")
		return ""
	}
}

// disconnect ends the server process: its pipe ends close.
func (f *fakeServer) disconnect() {
	_ = f.stdout.Close()
	_ = f.stdin.Close()
	<-f.done
}

type sseClient struct {
	resp *http.Response
	rd   *bufio.Reader
}

func connectSSE(t *testing.T, base string) *sseClient {
	t.Helper()
	resp, err := http.Get(base + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return &sseClient{resp: resp, rd: bufio.NewReader(resp.Body)}
}

// next reads one event and returns its name and data.
func (c *sseClient) next(t *testing.T) (event, data string) {
	t.Helper()
	for {
		line, err := c.rd.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the event stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && event != "":
			return event, data
		}
	}
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func status(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// startRun runs the bridge on a free port and returns its base URL, its pipe
// directory, and what stops it and reports its result.
func startRun(t *testing.T) (base, dir string, cancel context.CancelFunc, runErr chan error) {
	t.Helper()
	dir = t.TempDir()
	port := freePort(t)
	b := New(dir, port, "/healthz")
	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	runErr = make(chan error, 1)
	go func() { runErr <- b.Run(ctx) }()
	base = fmt.Sprintf("http://127.0.0.1:%d", port)

	waitFor(t, "the pipes", func() bool {
		st, err := os.Stat(filepath.Join(dir, "stdout"))
		return err == nil && st.Mode()&os.ModeNamedPipe != 0
	})
	waitFor(t, "the HTTP server", func() bool { return status(t, base+"/healthz") != 0 })
	return base, dir, cancel, runErr
}

// connectAndHandshake connects a fake server and checks that the bridge starts
// and finishes the MCP handshake by itself.
func connectAndHandshake(t *testing.T, dir string) *fakeServer {
	t.Helper()
	server := startFakeServer(t, dir)
	if line := server.nextLine(t); !strings.Contains(line, `"method":"initialize"`) || !strings.Contains(line, bridgeInitID) {
		t.Fatalf("the bridge did not start the handshake: %s", line)
	}
	if line := server.nextLine(t); !strings.Contains(line, "notifications/initialized") {
		t.Fatalf("the bridge did not finish the handshake: %s", line)
	}
	return server
}

// requestAndReply sends one request through a new SSE session and checks that the
// server gets it and the reply returns with the client's own id.
func requestAndReply(t *testing.T, base string, server *fakeServer) *sseClient {
	t.Helper()
	client := connectSSE(t, base)
	event, endpoint := client.next(t)
	if event != "endpoint" || !strings.HasPrefix(endpoint, "/message?sessionId=") {
		t.Fatalf("first event = %q %q", event, endpoint)
	}
	if code := post(t, base+endpoint, `{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`); code != http.StatusAccepted {
		t.Fatalf("POST /message = %d, want 202", code)
	}
	if line := server.nextLine(t); !strings.Contains(line, "tools/list") {
		t.Fatalf("the server did not get the request: %s", line)
	}
	event, data := client.next(t)
	if event != "message" || !strings.Contains(data, `"id":"abc"`) || !strings.Contains(data, `"echo":"tools/list"`) {
		t.Fatalf("reply = %q %q, want the client's own id and the server's result", event, data)
	}
	return client
}

// awaitAnnouncement reads events until the client is told to list tools again.
// The new server's answer to the bridge's own initialize is broadcast as well and
// comes first.
func awaitAnnouncement(t *testing.T, client *sseClient) {
	t.Helper()
	for {
		event, data := client.next(t)
		if event != "message" {
			t.Fatalf("after a restart the client got a %q event", event)
		}
		if data == toolsListChanged {
			return
		}
	}
}

// TestRun_ServesAServerThroughItsPipesAndAnnouncesARestart drives the whole sidecar:
// the pipes appear, the bridge starts the handshake itself, /readyz turns 200 once
// the server answers, a client's request reaches the server and its reply comes
// back to that client with the client's own id, and a restarted server makes the
// client list tools again.
func TestRun_ServesAServerThroughItsPipesAndAnnouncesARestart(t *testing.T) {
	base, dir, cancel, runErr := startRun(t)
	if code := status(t, base+"/healthz"); code != http.StatusServiceUnavailable {
		t.Fatalf("no server yet: /healthz = %d, want 503", code)
	}

	first := connectAndHandshake(t, dir)
	waitFor(t, "/readyz 200", func() bool { return status(t, base+"/readyz") == http.StatusOK })
	if code := status(t, base+"/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	client := requestAndReply(t, base, first)

	first.disconnect()
	waitFor(t, "/healthz 503 after the server left", func() bool { return status(t, base+"/healthz") == http.StatusServiceUnavailable })

	second := connectAndHandshake(t, dir)
	awaitAnnouncement(t, client)

	// Run notices the cancel between pipe sessions, so it returns once the server's
	// session has ended.
	cancel()
	second.disconnect()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v after cancel", err)
		}
	case <-time.After(safetyDeadline):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRun_FailsWhenThePipeDirIsMissing(t *testing.T) {
	b := New(filepath.Join(t.TempDir(), "absent"), freePort(t), "/healthz")
	if err := b.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded without a pipe directory")
	}
}

func TestMakePipes_AcceptsPipesLeftByAPreviousStart(t *testing.T) {
	b := New(t.TempDir(), 0, "/healthz")
	if err := b.makePipes(); err != nil {
		t.Fatal(err)
	}
	if err := b.makePipes(); err != nil {
		t.Fatalf("second start: %v", err)
	}
}

func TestPipeSession_StopsWhenTheContextEndsBeforeAServerConnects(t *testing.T) {
	b := New(t.TempDir(), 0, "/healthz")
	if err := b.makePipes(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.pipeSession(ctx); err != context.Canceled {
		t.Fatalf("pipeSession = %v, want context.Canceled", err)
	}
	if b.connected.Load() {
		t.Fatal("connected without a server")
	}
}

func TestPipeSession_ReportsAMissingPipe(t *testing.T) {
	b := New(t.TempDir(), 0, "/healthz")
	if err := b.pipeSession(context.Background()); err == nil {
		t.Fatal("pipeSession succeeded without pipes")
	}
}

func TestSend_DropsForASlowClientInsteadOfBlocking(t *testing.T) {
	ch := make(chan []byte, 1)
	send(ch, []byte("one"))
	send(ch, []byte("two"))
	if got := string(<-ch); got != "one" {
		t.Fatalf("got %q, want the first message kept", got)
	}
	select {
	case extra := <-ch:
		t.Fatalf("the second message was queued: %s", extra)
	default:
	}
}

func TestSendBridgeInitialize_WithoutAServerWritesNothingAndStillTracksTheRequest(t *testing.T) {
	b := New(t.TempDir(), 0, "/healthz")
	b.sendBridgeInitialize()
	if _, ok := b.pendingInits.Load(`"` + bridgeInitID + `"`); !ok {
		t.Fatal("the initialize request id was not tracked")
	}
}

func TestSendBridgeInitialize_WritesTheHandshakeToTheServer(t *testing.T) {
	b := New(t.TempDir(), 0, "/healthz")
	var sb strings.Builder
	b.stdin = nopWriteCloser{&sb}
	b.sendBridgeInitialize()
	var rpc struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &rpc); err != nil {
		t.Fatalf("not JSON: %q", sb.String())
	}
	if rpc.Method != "initialize" || rpc.ID != bridgeInitID {
		t.Fatalf("wrote %+v", rpc)
	}
}
