package bridge

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
)

const (
	headerCORS        = "Access-Control-Allow-Origin"
	headerContentType = "Content-Type"
)

// generateSessionID creates a random session identifier for MCP SSE clients.
func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Bridge creates named pipes (FIFOs) and bridges them to HTTP/SSE endpoints.
// It runs as a Kubernetes native sidecar alongside the MCP server container.
// The MCP server's stdin/stdout are redirected to the pipes via shell redirection.
//
// The bridge exposes three HTTP probes that gate Kubernetes lifecycle:
//
//   - /healthz       - bridge process alive AND pipes connected to MCP
//     (liveness probe target - restart on failure)
//   - /readyz        - MCP server has completed its initialize handshake
//     and is accepting tool calls (readiness probe target -
//     gates Service routing) + startupProbe target for the
//     long cold-start init window
//
// The init-handshake gate matters because stdio MCP servers (e.g. FastMCP)
// open their stdin/stdout pipes EARLY in startup - well before they've
// finished registering tools and processing the MCP `initialize` request.
// Without /readyz, the Kubernetes Service would route tool calls to a pod
// whose pipes are connected but whose MCP server is still initialising,
// producing `Failed to validate request: Received request before
// initialization was complete` errors. /readyz tracks the actual MCP
// initialize-request -> response cycle observed in the bridge.
type Bridge struct {
	pipeDir    string
	port       int
	healthPath string

	// True once the MCP server has connected to both pipes. Liveness signal.
	connected atomic.Bool

	// True after the bridge has observed a successful MCP `initialize`
	// response come back from the server. The flag resets on every pipe
	// disconnect (so a restarted MCP server appears un-ready until it
	// completes a fresh handshake). /readyz reads this; Kubernetes
	// readinessProbe + startupProbe target /readyz.
	initialized atomic.Bool

	// Outstanding `initialize` request IDs - populated when the bridge
	// observes an outgoing initialize request, consulted when a response
	// comes back so we can mark `initialized` only after the actual
	// MCP handshake completes (not on any random pass-through response).
	pendingInits sync.Map // string|float64 (JSON-RPC ID) -> struct{}

	// Current stdin pipe writer (protected by pipeMu)
	pipeMu sync.Mutex
	stdin  io.WriteCloser

	// SSE clients by session id, and the routing of replies back to them
	clientMu sync.Mutex
	clients  map[string]chan []byte
	msgID    atomic.Int64
	router   *router
}

// New creates a new Bridge instance.
func New(pipeDir string, port int, healthPath string) *Bridge {
	return &Bridge{
		pipeDir:    pipeDir,
		port:       port,
		healthPath: healthPath,
		clients:    make(map[string]chan []byte),
		router:     newRouter(),
	}
}

// Run copies the bridge binary to the pipe dir (for exec mode), creates FIFOs,
// starts the HTTP server, and loops reconnecting to pipes. Blocks until context is cancelled.
func (b *Bridge) Run(ctx context.Context) error {
	if err := b.installSelf(); err != nil {
		return err
	}
	if err := b.makePipes(); err != nil {
		return err
	}

	// Start HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", b.handleSSE)
	mux.HandleFunc("/message", b.handleMessage)
	mux.HandleFunc(b.healthPath, b.handleHealth)
	// /readyz: MCP server has completed its initialize handshake and is
	// accepting tool calls. Distinct from /healthz which only tracks
	// pipe connection - the MCP server can be connected but not yet
	// initialized for ~30-60s during cold start (FastMCP, etc.). The
	// kubemoot operator wires this as readinessProbe + startupProbe on
	// the bridge container of every MCPServer pod.
	mux.HandleFunc("/readyz", b.handleReady)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", b.port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()

	go func() {
		log.Printf("MCP bridge listening on :%d (health: %s, pipes: %s)", b.port, b.healthPath, b.pipeDir)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	// Main loop: connect to pipes, serve, reconnect on MCP server restart
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		log.Printf("Waiting for MCP server to connect to pipes...")
		if err := b.pipeSession(ctx); err != nil {
			log.Printf("Pipe session ended: %v", err)
		}
		b.connected.Store(false)
	}
}

// installSelf copies the bridge binary into the pipe dir so the main container can
// run it in exec mode, which removes the need for `sh` in the MCP server image.
func (b *Bridge) installSelf() error {
	selfPath, err := os.Executable()
	if err != nil {
		return nil
	}
	dst := filepath.Join(b.pipeDir, filepath.Base(selfPath))
	if err := installFile(selfPath, dst); err != nil {
		return fmt.Errorf("copy bridge binary to pipe dir: %w", err)
	}
	log.Printf("Copied bridge binary to %s", dst)
	return nil
}

// makePipes creates the stdin and stdout FIFOs with world read/write permissions,
// accepting ones left by a previous start of the sidecar.
func (b *Bridge) makePipes() error {
	for _, name := range []string{"stdin", "stdout"} {
		p := filepath.Join(b.pipeDir, name)
		if err := syscall.Mkfifo(p, 0666); err != nil && !os.IsExist(err) {
			return fmt.Errorf("mkfifo %s: %w", p, err)
		}
		if err := os.Chmod(p, 0666); err != nil {
			return fmt.Errorf("chmod %s: %w", p, err)
		}
	}
	return nil
}

// pipeSession opens the FIFOs (blocking until the MCP server connects),
// reads from stdout, and broadcasts to SSE clients. Returns on disconnect.
func (b *Bridge) pipeSession(ctx context.Context) error {
	stdinPath := filepath.Join(b.pipeDir, "stdin")
	stdoutPath := filepath.Join(b.pipeDir, "stdout")

	// Open both pipes concurrently - each blocks until the MCP server opens its end
	type openResult struct {
		file *os.File
		err  error
	}

	stdoutCh := make(chan openResult, 1)
	stdinCh := make(chan openResult, 1)

	go func() {
		f, err := os.OpenFile(stdoutPath, os.O_RDONLY, 0)
		stdoutCh <- openResult{f, err}
	}()

	go func() {
		f, err := os.OpenFile(stdinPath, os.O_WRONLY, 0)
		stdinCh <- openResult{f, err}
	}()

	var stdoutFile, stdinFile *os.File

	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-stdoutCh:
			if r.err != nil {
				return fmt.Errorf("open stdout pipe: %w", r.err)
			}
			stdoutFile = r.file
		case r := <-stdinCh:
			if r.err != nil {
				return fmt.Errorf("open stdin pipe: %w", r.err)
			}
			stdinFile = r.file
		}
	}

	defer func() { _ = stdoutFile.Close() }()
	defer func() { _ = stdinFile.Close() }()

	b.pipeMu.Lock()
	b.stdin = stdinFile
	b.pipeMu.Unlock()

	b.connected.Store(true)
	// Reset init state for the fresh MCP server process. The previous
	// initialization (if any, before a restart) belonged to the old
	// process; the new one needs its own handshake before /readyz
	// returns 200.
	b.initialized.Store(false)
	b.pendingInits.Range(func(k, _ any) bool {
		b.pendingInits.Delete(k)
		return true
	})

	// Bridge-driven initialize handshake - closes the chicken-and-egg
	// deadlock with /readyz-based readiness probes. The first /readyz =>
	// 200 must happen BEFORE any external client connects, because
	// Kubernetes Services only route to ready pods and external clients
	// only connect through the Service. So the bridge itself acts as the
	// first MCP client: send `initialize` now, observe the response via
	// the readStdoutPipe loop (which calls maybeMarkInitialized to flip
	// the flag), then send the notifications/initialized notification to
	// complete the handshake per MCP protocol. Idempotent: real clients
	// connecting later may send their own initialize too; servers like
	// FastMCP handle re-initialize gracefully (treat as no-op).
	go b.sendBridgeInitialize()
	log.Printf("MCP server connected via pipes; bridge initiating MCP handshake")

	// Read JSON-RPC messages from stdout pipe and broadcast to SSE clients.
	// The initial buffer is 64KB; it grows on demand up to the 16MB cap.
	// 16MB handles very large MCP tool responses (e.g. Prometheus metrics metadata
	// listings can exceed 4MB on a busy cluster with many exporters).
	// Using a small initial size avoids reserving 16MB per sidecar pod at startup.
	const scannerMaxBytes = 16 * 1024 * 1024 // 16MB cap

	err := b.readStdoutPipe(stdoutFile, scannerMaxBytes)

	b.pipeMu.Lock()
	b.stdin = nil
	b.pipeMu.Unlock()

	if err != nil {
		return err
	}
	return fmt.Errorf("MCP server disconnected (EOF)")
}

// readStdoutPipe reads newline-delimited JSON-RPC messages from the stdout pipe
// and broadcasts them to SSE clients. It handles bufio.ErrTooLong as a recoverable
// error - oversized messages are logged and skipped without ending the session.
func (b *Bridge) readStdoutPipe(r io.Reader, maxBytes int) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		if !json.Valid(line) {
			log.Printf("Non-JSON output from MCP server (skipping): %s", line)
			continue
		}

		// Inspect the JSON-RPC response: if its ID matches a pending
		// `initialize` request AND it carries a `result` (not an error),
		// the MCP server has completed its handshake and is ready to
		// serve tool calls. Flip `initialized` so /readyz returns 200.
		// Best-effort: parse failures fall through to broadcast normally.
		b.maybeMarkInitialized(line)

		session, data := b.router.inbound(line)
		b.deliver(session, append([]byte(nil), data...))
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			log.Printf("WARNING: MCP server response exceeded %d MB buffer, skipping oversized message", maxBytes/(1024*1024))
			// Recover by creating a new scanner and continuing to read.
			// The oversized line has been consumed from the reader; the next
			// line should be a normal-sized JSON-RPC message.
			return b.readStdoutPipe(r, maxBytes)
		}
		return fmt.Errorf("reading stdout pipe: %w", err)
	}
	return nil
}

// deliver sends a message to one session, or to every session when session is
// empty. A reply whose session has disconnected is dropped.
func (b *Bridge) deliver(session string, data []byte) {
	b.clientMu.Lock()
	defer b.clientMu.Unlock()

	if session != "" {
		if ch, ok := b.clients[session]; ok {
			send(ch, data)
		}
		return
	}
	for _, ch := range b.clients {
		send(ch, data)
	}
}

func send(ch chan []byte, data []byte) {
	select {
	case ch <- data:
	default:
		log.Printf("Dropping message for slow SSE client")
	}
}

// handleSSE establishes an SSE connection and streams MCP server responses.
func (b *Bridge) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set(headerContentType, "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set(headerCORS, "*")

	// Send endpoint event with session ID so MCP clients know where to POST messages
	sessionID := generateSessionID()
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /message?sessionId=%s\n\n", sessionID)
	flusher.Flush()

	ch := make(chan []byte, 64)
	b.clientMu.Lock()
	b.clients[sessionID] = ch
	b.clientMu.Unlock()

	defer func() {
		b.clientMu.Lock()
		delete(b.clients, sessionID)
		b.clientMu.Unlock()
		b.router.forget(sessionID)
	}()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-ch:
			id := b.msgID.Add(1)
			_, _ = fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", id, data)
			flusher.Flush()
		}
	}
}

// handleMessage accepts JSON-RPC requests and writes them to the MCP server's stdin pipe.
func (b *Bridge) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set(headerCORS, "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", headerContentType)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !b.connected.Load() {
		http.Error(w, "MCP server not connected", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	if !json.Valid(body) {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Give a request an id unique across sessions so its reply returns to this
	// session only; then track outgoing `initialize` requests (by that id) so
	// readStdoutPipe can match the response and flip `initialized`.
	body = b.router.outbound(r.URL.Query().Get("sessionId"), body)
	b.trackOutgoingInitialize(body)

	b.pipeMu.Lock()
	stdin := b.stdin
	b.pipeMu.Unlock()

	if stdin == nil {
		http.Error(w, "MCP server not connected", http.StatusServiceUnavailable)
		return
	}

	// Write JSON-RPC message to stdin pipe followed by newline
	if _, err := fmt.Fprintf(stdin, "%s\n", body); err != nil {
		http.Error(w, "Failed to write to MCP server", http.StatusInternalServerError)
		return
	}

	w.Header().Set(headerCORS, "*")
	w.WriteHeader(http.StatusAccepted)
}

// handleHealth returns 200 if MCP server is connected, 503 otherwise.
// Liveness-style probe - bridge process is alive AND its stdio pipes are
// connected. Does NOT track initialize-handshake completion (use /readyz
// for that). Intended target for Kubernetes livenessProbe.
func (b *Bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerCORS, "*")
	w.Header().Set(headerContentType, "application/json")
	if b.connected.Load() {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"status":"waiting for MCP server"}`)
	}
}

// handleReady returns 200 only when the MCP server has completed its
// `initialize` handshake. Distinct from /healthz: a freshly-started
// FastMCP (or similar) opens its stdin/stdout pipes early but takes
// 30-60s of cold-start work before it can answer tool calls. During
// that window, /healthz returns 200 (pipes are connected) but /readyz
// returns 503 (no initialize response observed yet). Intended target
// for Kubernetes readinessProbe AND startupProbe - the former gates
// Service routing, the latter gives generous time for the initial
// handshake without tripping liveness.
func (b *Bridge) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerCORS, "*")
	w.Header().Set(headerContentType, "application/json")
	if b.connected.Load() && b.initialized.Load() {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ready"}`)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	if !b.connected.Load() {
		_, _ = fmt.Fprint(w, `{"status":"waiting for MCP server"}`)
	} else {
		_, _ = fmt.Fprint(w, `{"status":"waiting for MCP initialize handshake"}`)
	}
}

// trackOutgoingInitialize records the JSON-RPC ID of an outgoing
// `initialize` request so the response can later be matched and
// `initialized` flipped. Best-effort - silently no-ops on parse
// failure or when the message isn't an initialize.
//
// JSON-RPC ID can be a string or number per spec; we store the raw
// JSON form ("\"id\"" or "1") as the key so matching is exact.
func (b *Bridge) trackOutgoingInitialize(body []byte) {
	var rpc struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return
	}
	if rpc.Method != "initialize" || len(rpc.ID) == 0 {
		return
	}
	b.pendingInits.Store(string(rpc.ID), struct{}{})
}

// maybeMarkInitialized inspects an incoming JSON-RPC response from the
// MCP server; if its ID matches a tracked outgoing `initialize`
// request AND it carries a non-error result, the MCP server has
// completed its handshake and is ready to serve tool calls. Flips
// `initialized` to true. Idempotent - subsequent matches are no-ops.
//
// When the matched response was the bridge's own initialize (id
// {@link #bridgeInitID}), also sends the protocol-required
// `notifications/initialized` to complete the handshake. Real-client
// initializes won't trigger that follow-up - they send their own
// notifications/initialized as part of their MCP protocol flow.
func (b *Bridge) maybeMarkInitialized(line []byte) {
	if b.initialized.Load() {
		return
	}
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(line, &rpc); err != nil {
		return
	}
	if len(rpc.ID) == 0 || len(rpc.Result) == 0 || len(rpc.Error) != 0 {
		return
	}
	idStr := string(rpc.ID)
	if _, ok := b.pendingInits.LoadAndDelete(idStr); !ok {
		return
	}
	b.initialized.Store(true)
	log.Printf("MCP server initialize handshake complete - /readyz now returns 200")
	// Complete the protocol handshake when the response was OURS.
	// Real clients send their own notifications/initialized via the SSE
	// handler; only the bridge's own initialize needs this follow-up.
	if idStr == "\""+bridgeInitID+"\"" {
		go b.sendBridgeInitializedNotification()
	}
}

// bridgeInitID is the JSON-RPC ID used for the bridge's own initialize
// request. Constant + recognizable so maybeMarkInitialized can tell
// bridge-initiated handshakes from real-client ones (only the
// bridge-initiated case needs the follow-up notifications/initialized).
const bridgeInitID = "kubemoot-bridge-init"

// sendBridgeInitialize writes the bridge's own MCP `initialize` JSON-RPC
// request to the MCP server's stdin pipe. Called when pipes connect so
// /readyz becomes reachable BEFORE any external client connects -
// breaking the chicken-and-egg deadlock that otherwise prevents
// readiness-probe-gated pods from ever entering Service rotation.
//
// Declares broad client capabilities so any subsequent real-client
// initialize is a no-op or refinement, not a new handshake the server
// must restart. Errors are logged but never fail the bridge - the
// handshake will simply not complete, /readyz will stay 503, and the
// Kubernetes liveness probe will eventually restart the pod via
// /healthz failure if the bridge can't write to the pipe at all.
func (b *Bridge) sendBridgeInitialize() {
	request := fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"initialize","params":`+
		`{"protocolVersion":"2024-11-05","capabilities":{"tools":{},"resources":{}},`+
		`"clientInfo":{"name":"kubemoot-mcp-bridge","version":"1.0"}}}`,
		bridgeInitID)
	b.pendingInits.Store("\""+bridgeInitID+"\"", struct{}{})
	b.pipeMu.Lock()
	stdin := b.stdin
	b.pipeMu.Unlock()
	if stdin == nil {
		log.Printf("Bridge initialize: stdin pipe not available (raced with disconnect)")
		return
	}
	if _, err := fmt.Fprintf(stdin, "%s\n", request); err != nil {
		log.Printf("Bridge initialize: failed to write to MCP stdin: %v", err)
		return
	}
	log.Printf("Bridge sent initialize request to MCP server; waiting for response")
}

// sendBridgeInitializedNotification writes the protocol-required
// `notifications/initialized` JSON-RPC notification after the server
// responds to the bridge's initialize request. Per MCP spec the client
// must send this to signal it's ready to send tool calls. Idempotent
// from the server's perspective; real clients will send their own
// notifications/initialized if they re-handshake.
func (b *Bridge) sendBridgeInitializedNotification() {
	const notification = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	b.pipeMu.Lock()
	stdin := b.stdin
	b.pipeMu.Unlock()
	if stdin == nil {
		return
	}
	if _, err := fmt.Fprintf(stdin, "%s\n", notification); err != nil {
		log.Printf("Bridge initialize: failed to send notifications/initialized: %v", err)
		return
	}
	log.Printf("Bridge handshake complete (sent notifications/initialized)")
}

// installFile puts a copy of src at dst by writing a temporary file in dst's
// directory and renaming it over dst. The rename replaces the directory entry,
// so it succeeds even while another process is executing the old dst: after a
// sidecar restart the main container is running the binary installed by the
// previous start, and writing into that file in place fails with ETXTBSY.
func installFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once the rename has succeeded

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
