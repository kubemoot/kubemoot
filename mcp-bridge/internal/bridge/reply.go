package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
)

const (
	// maxReplyEnv names the environment variable that overrides the reply size cap.
	maxReplyEnv = "KUBEMOOT_MCP_BRIDGE_MAX_REPLY_BYTES"

	// defaultMaxReplyBytes caps one JSON-RPC reply line at 1 MiB. A model's context
	// window holds about 128 KB of text, so a larger tool result cannot be used by
	// any agent. The sidecar runs with a 64Mi memory limit and a reply is held
	// several times while it is validated, routed, copied and encoded for SSE, so
	// 1 MiB needs roughly 5 MiB and leaves the limit well clear.
	defaultMaxReplyBytes = 1 << 20

	// readChunkBytes is the size of the stdout read buffer.
	readChunkBytes = 64 * 1024

	// idPrefixBytes is how much of the start of a discarded reply is kept to find its id.
	idPrefixBytes = 1024

	// logPrefixBytes is how much of a non-JSON line is written to the log.
	logPrefixBytes = 200

	// oversizedErrorCode is the JSON-RPC error code sent for a discarded reply.
	oversizedErrorCode = -32000
)

// maxReplyBytesFromEnv returns the reply size cap, from the environment when it
// holds a positive integer.
func maxReplyBytesFromEnv() int {
	v := os.Getenv(maxReplyEnv)
	if v == "" {
		return defaultMaxReplyBytes
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("Ignoring invalid %s=%q; using %d", maxReplyEnv, v, defaultMaxReplyBytes)
		return defaultMaxReplyBytes
	}
	return n
}

// readStdoutPipe reads newline-delimited JSON-RPC messages from the stdout pipe
// and broadcasts them to SSE clients. A line longer than maxBytes is discarded as
// it streams, never buffered past maxBytes, and the waiting caller is answered
// with a JSON-RPC error. The bridge then keeps serving the next message.
func (b *Bridge) readStdoutPipe(r io.Reader, maxBytes int) error {
	reader := bufio.NewReaderSize(r, readChunkBytes)
	for {
		line, prefix, err := readBoundedLine(reader, maxBytes)
		switch {
		case prefix != nil:
			b.rejectOversized(prefix, maxBytes)
		case len(line) > 0:
			b.handleLine(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading stdout pipe: %w", err)
		}
	}
}

// readBoundedLine reads up to the next newline. When the line is within maxBytes
// it is returned. When it is longer, the rest is read and thrown away and only
// the start of the line is returned as prefix.
func readBoundedLine(r *bufio.Reader, maxBytes int) (line, prefix []byte, err error) {
	var buf []byte
	oversized := false
	for {
		chunk, rerr := r.ReadSlice('\n')
		if !oversized {
			if len(buf)+len(chunk) > maxBytes+1 {
				oversized = true
				prefix = append([]byte(nil), firstBytes(buf, chunk, idPrefixBytes)...)
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		if oversized {
			return nil, prefix, rerr
		}
		return bytes.TrimRight(buf, "\r\n"), nil, rerr
	}
}

// firstBytes returns up to n bytes taken from buf followed by chunk.
func firstBytes(buf, chunk []byte, n int) []byte {
	joined := append(append(make([]byte, 0, n), buf[:min(len(buf), n)]...), chunk[:min(len(chunk), n-min(len(buf), n))]...)
	return joined
}

// handleLine validates one reply line and routes it to its SSE session.
func (b *Bridge) handleLine(line []byte) {
	if len(line) == 0 {
		return
	}
	if !json.Valid(line) {
		log.Printf("Non-JSON output from MCP server (skipping, %d bytes): %s", len(line), logPrefix(line))
		return
	}

	// If the ID matches a pending `initialize` request and the reply carries a
	// `result`, the MCP server has completed its handshake: flip `initialized`
	// so /readyz returns 200.
	b.maybeMarkInitialized(line)
	b.route(line)
}

// route sends a line to the session it answers, or to every session.
func (b *Bridge) route(line []byte) {
	session, data, drop := b.router.inbound(line)
	if drop {
		log.Printf("Dropping a reply for a session that has disconnected")
		return
	}
	b.deliver(session, append([]byte(nil), data...))
}

// rejectOversized answers the caller of a discarded reply with a JSON-RPC error
// carrying the same id, routed like a normal reply. Without an id in the prefix
// there is nobody to answer and the reply is only logged.
func (b *Bridge) rejectOversized(prefix []byte, maxBytes int) {
	id, ok := responseID(prefix)
	if !ok {
		log.Printf("Discarded an MCP server message over %d bytes with no reply id; starts: %s", maxBytes, logPrefix(prefix))
		return
	}
	log.Printf("Discarded an MCP server reply over %d bytes (id %s); answering the caller with an error", maxBytes, id)
	b.route(oversizedError(id, maxBytes))
}

// oversizedError builds the JSON-RPC error that stands in for a discarded reply.
func oversizedError(id json.RawMessage, maxBytes int) []byte {
	msg := fmt.Sprintf("The tool result was larger than the %d KB limit and was not delivered. "+
		"Ask for less: filter, select specific fields, or request a shorter range.", maxBytes/1024)
	out, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": oversizedErrorCode, "message": msg},
	})
	return out
}

// responseID reads the id of a JSON-RPC response from the start of its line. The
// id precedes the result in a response, so the bounded prefix holds it. Only
// string and number ids are accepted, and a message with a method is not a reply.
func responseID(prefix []byte) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(prefix))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		keyTok, err := dec.Token()
		key, isKey := keyTok.(string)
		if err != nil || !isKey || key == "method" {
			return nil, false
		}
		var val json.RawMessage
		if dec.Decode(&val) != nil {
			return nil, false
		}
		if key == "id" {
			return val, validID(val)
		}
	}
	return nil, false
}

// validID reports whether raw is a JSON string or number.
func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	return raw[0] == '"' || raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')
}

// logPrefix returns the start of a line, shortened for logging.
func logPrefix(line []byte) string {
	if len(line) <= logPrefixBytes {
		return strings.ToValidUTF8(string(line), "?")
	}
	return strings.ToValidUTF8(string(line[:logPrefixBytes]), "?") + "..."
}
