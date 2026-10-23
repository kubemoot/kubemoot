package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"os"
	"runtime"
	"strings"
	"testing"
)

const testCap = 100 * 1024

func drain(ch chan []byte) []string {
	close(ch)
	var out []string
	for m := range ch {
		out = append(out, string(m))
	}
	return out
}

func newTestBridge() (*Bridge, chan []byte) {
	b := New("/tmp", 8080, "/healthz")
	ch := make(chan []byte, 64)
	b.clientMu.Lock()
	b.clients["test"] = ch
	b.clientMu.Unlock()
	return b, ch
}

// replyOfSize returns a reply line whose length, without the newline, is n.
func replyOfSize(idJSON string, n int) string {
	head := `{"jsonrpc":"2.0","id":` + idJSON + `,"result":{"d":"`
	tail := `"}}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

const (
	truncatedResult = `{"result":{"aaaa":"bbb`
	abc             = "abc"
)

const normalReply = `{"jsonrpc":"2.0","id":9,"result":"ok"}`

func assertOversizedError(t *testing.T, msg, wantID string) {
	t.Helper()
	var got struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(msg), &got); err != nil {
		t.Fatalf("not JSON: %v: %.200s", err, msg)
	}
	if string(got.ID) != wantID {
		t.Errorf("id = %s, want %s", got.ID, wantID)
	}
	if got.Error.Code != oversizedErrorCode || !strings.Contains(got.Error.Message, "Ask for less") {
		t.Errorf("unexpected error: %+v", got.Error)
	}
}

func TestReadStdoutPipe_NormalAndUnderCap(t *testing.T) {
	b, ch := newTestBridge()
	under := replyOfSize("1", testCap)
	input := normalReply + "\n" + under + "\n"
	if err := b.readStdoutPipe(strings.NewReader(input), testCap); err != nil {
		t.Fatal(err)
	}
	got := drain(ch)
	if len(got) != 2 || got[0] != normalReply || got[1] != under {
		t.Fatalf("got %d messages", len(got))
	}
}

func TestReadStdoutPipe_OverCapAnswersCallerAndContinues(t *testing.T) {
	for _, id := range []string{"7", `"abc-1"`} {
		b, ch := newTestBridge()
		over := replyOfSize(id, testCap+1)
		input := over + "\n" + normalReply + "\n"
		if err := b.readStdoutPipe(strings.NewReader(input), testCap); err != nil {
			t.Fatal(err)
		}
		got := drain(ch)
		if len(got) != 2 {
			t.Fatalf("id %s: got %d messages, want error plus next reply", id, len(got))
		}
		assertOversizedError(t, got[0], id)
		if got[1] != normalReply {
			t.Errorf("next message not delivered: %.80s", got[1])
		}
	}
}

func TestReadStdoutPipe_OverCapRoutedToRequestingSession(t *testing.T) {
	b, ch := newTestBridge()
	other := make(chan []byte, 8)
	b.clientMu.Lock()
	b.clients["other"] = other
	b.clientMu.Unlock()

	out := b.router.outbound("test", []byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call"}`))
	var sent struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(out, &sent); err != nil {
		t.Fatal(err)
	}
	over := replyOfSize(string(sent.ID), testCap+10)
	if err := b.readStdoutPipe(strings.NewReader(over+"\n"), testCap); err != nil {
		t.Fatal(err)
	}
	if got := drain(ch); len(got) != 1 {
		t.Fatalf("requesting session got %d messages", len(got))
	} else {
		assertOversizedError(t, got[0], "5")
	}
	if got := drain(other); len(got) != 0 {
		t.Errorf("other session received %d messages", len(got))
	}
}

func TestReadStdoutPipe_OverCapWithoutIDIsDropped(t *testing.T) {
	b, ch := newTestBridge()
	over := `{"jsonrpc":"2.0","method":"notifications/x","params":"` + strings.Repeat("y", testCap) + `"}`
	input := over + "\n" + normalReply + "\n"
	if err := b.readStdoutPipe(strings.NewReader(input), testCap); err != nil {
		t.Fatal(err)
	}
	got := drain(ch)
	if len(got) != 1 || got[0] != normalReply {
		t.Fatalf("got %d messages, want only the normal reply", len(got))
	}
}

func TestReadStdoutPipe_NonJSONLoggedTruncated(t *testing.T) {
	b, ch := newTestBridge()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	junk := strings.Repeat("z", 5000)
	if err := b.readStdoutPipe(strings.NewReader(junk+"\n"+normalReply+"\n"), testCap); err != nil {
		t.Fatal(err)
	}
	if got := drain(ch); len(got) != 1 {
		t.Fatalf("got %d messages", len(got))
	}
	out := logs.String()
	if !strings.Contains(out, "5000 bytes") || len(out) > 1000 {
		t.Errorf("log not truncated (%d bytes): %.300s", len(out), out)
	}
}

func TestResponseID(t *testing.T) {
	cases := map[string]struct {
		head, tail string
		want       string
		ok         bool
	}{
		"number":         {`{"jsonrpc":"2.0","id":12,"result":{"a":`, "", "12", true},
		"string":         {`{"id":"kmb-3","jsonrpc":"2.0","result":`, "", `"kmb-3"`, true},
		"escaped quote":  {`{"jsonrpc":"2.0","id":"a\"b","result":`, "", `"a\"b"`, true},
		"negative":       {`{"id":-4,"result":`, "", "-4", true},
		"float":          {`{"id":1.5,"result":`, "", "1.5", true},
		"null id":        {`{"jsonrpc":"2.0","id":null,"result":1}`, "", "", false},
		"object id":      {`{"id":{"a":1},"result":1}`, "", "", false},
		"boolean id":     {`{"id":true,"result":1}`, "", "", false},
		"method":         {`{"jsonrpc":"2.0","method":"x","id":3`, `,"id":3}`, "", false},
		"result first":   {truncatedResult, `xxx"},"jsonrpc":"2.0","id":7}`, "7", true},
		"tail string":    {truncatedResult, `x"},"id":"q\"r"}`, `"q\"r"`, true},
		"tail nested id": {truncatedResult, `xxx","id":7}}`, "", false},
		"not object":     {`[1,2`, ``, "", false},
		"empty":          {``, ``, "", false},
	}
	for name, c := range cases {
		id, ok := responseID([]byte(c.head), []byte(c.tail))
		if ok != c.ok || (ok && string(id) != c.want) {
			t.Errorf("%s: got %s %v, want %s %v", name, id, ok, c.want, c.ok)
		}
	}
}

func TestReadStdoutPipe_IDAfterOverCapResult(t *testing.T) {
	for _, id := range []string{"7", `"a\"b"`, "-3", "1.5"} {
		b, ch := newTestBridge()
		over := `{"result":{"d":"` + strings.Repeat("x", testCap+10) + `"},"jsonrpc":"2.0","id":` + id + `}`
		if err := b.readStdoutPipe(strings.NewReader(over+"\n"+normalReply+"\n"), testCap); err != nil {
			t.Fatal(err)
		}
		got := drain(ch)
		if len(got) != 2 {
			t.Fatalf("id %s: got %d messages", id, len(got))
		}
		assertOversizedError(t, got[0], id)
	}
}

func TestReadStdoutPipe_CapBoundaryTerminators(t *testing.T) {
	cases := []struct {
		name, term string
		size       int
		rejected   bool
	}{
		{"LF at cap", "\n", testCap, false},
		{"LF over cap", "\n", testCap + 1, true},
		{"CRLF at cap", "\r\n", testCap, false},
		{"CRLF over cap", "\r\n", testCap + 1, true},
		{"EOF at cap", "", testCap, false},
		{"EOF over cap", "", testCap + 1, true},
	}
	for _, c := range cases {
		b, ch := newTestBridge()
		line := replyOfSize("1", c.size)
		if err := b.readStdoutPipe(strings.NewReader(line+c.term), testCap); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := drain(ch)
		if len(got) != 1 {
			t.Fatalf("%s: got %d messages", c.name, len(got))
		}
		if c.rejected {
			assertOversizedError(t, got[0], "1")
		} else if got[0] != line {
			t.Errorf("%s: line altered or rejected: %.100s", c.name, got[0])
		}
	}
}

func TestReadBoundedLine(t *testing.T) {
	const max = 10
	cases := []struct {
		name, in string
		line     string
		size     int // 0 when the line is returned
	}{
		{"short", abc + "\n", abc, 0},
		{"crlf", abc + "\r\n", abc, 0},
		{"at cap", "0123456789\r\n", "0123456789", 0},
		{"over cap", "0123456789A\n", "", 11},
		{"over cap crlf", "0123456789A\r\n", "", 11},
		{"over cap no newline", strings.Repeat("y", 3*readChunkBytes), "", 3 * readChunkBytes},
	}
	for _, c := range cases {
		line, over, _ := readBoundedLine(bufio.NewReaderSize(strings.NewReader(c.in), readChunkBytes), max)
		if c.size == 0 {
			if over != nil || string(line) != c.line {
				t.Errorf("%s: got %q %+v", c.name, line, over)
			}
			continue
		}
		if over == nil || over.size != c.size {
			t.Errorf("%s: over = %+v, want size %d", c.name, over, c.size)
		}
	}
}

func TestReadBoundedLine_KeepsHeadAndTail(t *testing.T) {
	in := "HEAD" + strings.Repeat("m", 3*readChunkBytes) + "TAIL\r\n"
	_, over, _ := readBoundedLine(bufio.NewReaderSize(strings.NewReader(in), readChunkBytes), 100)
	if over == nil || !strings.HasPrefix(string(over.head), "HEAD") || !strings.HasSuffix(string(over.tail), "TAIL") {
		t.Fatalf("head or tail wrong: %+v", over)
	}
	if len(over.head) > idPrefixBytes || len(over.tail) > idPrefixBytes {
		t.Errorf("head %d or tail %d over %d", len(over.head), len(over.tail), idPrefixBytes)
	}
	if over.size != len(in)-2 {
		t.Errorf("size = %d, want %d", over.size, len(in)-2)
	}
}

func TestFirstAndLastBytes(t *testing.T) {
	b := []byte("abcdef")
	if string(firstBytes(b, 3)) != "abc" || string(firstBytes(b, 99)) != "abcdef" || len(firstBytes(nil, 3)) != 0 {
		t.Error("firstBytes")
	}
	if string(lastBytes(b, 3)) != "def" || string(lastBytes(b, 99)) != "abcdef" || len(lastBytes(nil, 3)) != 0 {
		t.Error("lastBytes")
	}
	f := firstBytes(b, 3)
	f[0] = 'Z'
	if b[0] != 'a' {
		t.Error("firstBytes aliases its input")
	}
}

func TestMaxReplyBytesFromEnv(t *testing.T) {
	t.Setenv(maxReplyEnv, "")
	if got := maxReplyBytesFromEnv(); got != defaultMaxReplyBytes {
		t.Errorf("default = %d", got)
	}
	t.Setenv(maxReplyEnv, "2048")
	if got := maxReplyBytesFromEnv(); got != 2048 {
		t.Errorf("override = %d", got)
	}
	for _, bad := range []string{"abc", "0", "-5"} {
		t.Setenv(maxReplyEnv, bad)
		if got := maxReplyBytesFromEnv(); got != defaultMaxReplyBytes {
			t.Errorf("%q = %d, want default", bad, got)
		}
	}
}

// An over-cap line must not be held in memory: reading 20 MB with a 100 KB cap
// allocates a small multiple of the cap, not the line size.
func TestReadStdoutPipe_OverCapDoesNotBufferTheLine(t *testing.T) {
	b, _ := newTestBridge()
	over := []byte(replyOfSize("1", 20*1024*1024) + "\n")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := b.readStdoutPipe(bytes.NewReader(over), testCap); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 4*testCap+1<<20 {
		t.Errorf("allocated %d bytes reading a 20 MB line with a %d byte cap", grown, testCap)
	}
}
