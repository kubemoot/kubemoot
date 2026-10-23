package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
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

// oversizedLine describes a reply line that was discarded: its first and last
// bytes (where a JSON-RPC id is found) and its length without the terminator.
type oversizedLine struct {
	head, tail []byte
	size       int
}

// readStdoutPipe reads newline-delimited JSON-RPC messages from the stdout pipe
// and broadcasts them to SSE clients. A line longer than maxBytes is discarded as
// it streams, never buffered past maxBytes, and the waiting caller is answered
// with a JSON-RPC error. The bridge then keeps serving the next message.
func (b *Bridge) readStdoutPipe(r io.Reader, maxBytes int) error {
	reader := bufio.NewReaderSize(r, readChunkBytes)
	for {
		line, over, err := readBoundedLine(reader, maxBytes)
		switch {
		case over != nil:
			b.rejectOversized(*over, maxBytes)
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

// readBoundedLine reads up to the next newline. A line whose content, without a
// trailing \r\n or \n, is within maxBytes is returned. A longer line is read to
// its end and thrown away; only its head, tail and size are returned.
func readBoundedLine(r *bufio.Reader, maxBytes int) (line []byte, over *oversizedLine, err error) {
	var buf []byte
	var o *oversizedLine
	for {
		chunk, rerr := r.ReadSlice('\n')
		if o != nil {
			o.size += len(chunk)
			o.tail = appendTail(o.tail, chunk, idPrefixBytes)
		} else {
			buf = append(buf, chunk...)
			// Room for a \r\n terminator; past it the content is surely over the cap.
			if len(buf) > maxBytes+2 {
				o = &oversizedLine{head: firstBytes(buf, idPrefixBytes), tail: lastBytes(buf, idPrefixBytes), size: len(buf)}
				buf = nil
			}
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		if o == nil {
			buf = trimTerminator(buf)
			if len(buf) > maxBytes {
				o = &oversizedLine{head: firstBytes(buf, idPrefixBytes), tail: lastBytes(buf, idPrefixBytes), size: len(buf)}
				return nil, o, rerr
			}
			return buf, nil, rerr
		}
		trimmed := trimTerminator(o.tail)
		o.size -= len(o.tail) - len(trimmed)
		o.tail = trimmed
		return nil, o, rerr
	}
}

// trimTerminator removes one trailing \n, then one trailing \r.
func trimTerminator(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}

// firstBytes returns a copy of up to the first n bytes of b.
func firstBytes(b []byte, n int) []byte {
	return append([]byte(nil), b[:min(len(b), n)]...)
}

// appendTail returns the last n bytes of tail followed by chunk, copying at most
// n bytes however large the chunk is.
func appendTail(tail, chunk []byte, n int) []byte {
	if len(chunk) >= n {
		return lastBytes(chunk, n)
	}
	return lastBytes(append(tail, chunk...), n)
}

// lastBytes returns a copy of up to the last n bytes of b.
func lastBytes(b []byte, n int) []byte {
	return append([]byte(nil), b[max(0, len(b)-n):]...)
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
// carrying the same id, routed like a normal reply. Without an id there is nobody
// to answer and the reply is only logged.
func (b *Bridge) rejectOversized(o oversizedLine, maxBytes int) {
	id, ok := responseID(o.head, o.tail)
	if !ok {
		log.Printf("Discarded an MCP server message of %d bytes (limit %d) with no reply id; starts: %s",
			o.size, maxBytes, logPrefix(o.head))
		return
	}
	log.Printf("Discarded an MCP server reply of %d bytes (limit %d, id %s); answering the caller with an error",
		o.size, maxBytes, id)
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

// trailingID matches a top-level id that ends a JSON-RPC object. A nested id is
// followed by more closing braces, so it does not match.
var trailingID = regexp.MustCompile(`,\s*"id"\s*:\s*("(?:[^"\\]|\\.)*"|-?[0-9][0-9.eE+-]*)\s*\}\s*$`)

// responseID finds the id of a JSON-RPC response from the head of its line, where
// serializers that write the id first put it, or from the tail, where serializers
// that write the result first put it. Only string and number ids are accepted, and
// a message with a method is not a reply.
func responseID(head, tail []byte) (json.RawMessage, bool) {
	id, state := headID(head)
	switch state {
	case idFound:
		return id, validID(id)
	case idNotReply:
		return nil, false
	}
	m := trailingID.FindSubmatch(tail)
	if m == nil {
		return nil, false
	}
	return json.RawMessage(m[1]), validID(m[1])
}

const (
	idUnknown = iota
	idFound
	idNotReply
)

// headID reads top-level keys from the possibly truncated start of a message
// until it reaches the id, a method, or a value too large to skip.
func headID(head []byte) (json.RawMessage, int) {
	dec := json.NewDecoder(bytes.NewReader(head))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, idNotReply
	}
	for dec.More() {
		keyTok, err := dec.Token()
		key, isKey := keyTok.(string)
		if err != nil || !isKey {
			return nil, idUnknown
		}
		if key == "method" {
			return nil, idNotReply
		}
		var val json.RawMessage
		if dec.Decode(&val) != nil {
			return nil, idUnknown
		}
		if key == "id" {
			return val, idFound
		}
	}
	return nil, idUnknown
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
