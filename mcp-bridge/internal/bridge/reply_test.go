package bridge

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
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
		in   string
		want string
		ok   bool
	}{
		"number":       {`{"jsonrpc":"2.0","id":12,"result":{"a":`, "12", true},
		"string":       {`{"id":"kmb-3","jsonrpc":"2.0","result":`, `"kmb-3"`, true},
		"null id":      {`{"jsonrpc":"2.0","id":null,"result":1}`, "", false},
		"method":       {`{"jsonrpc":"2.0","method":"x","id":3`, "", false},
		"result first": {`{"result":{"aaaa":"bbb`, "", false},
		"not object":   {`[1,2`, "", false},
		"empty":        {``, "", false},
	}
	for name, c := range cases {
		id, ok := responseID([]byte(c.in))
		if ok != c.ok || (ok && string(id) != c.want) {
			t.Errorf("%s: got %s %v, want %s %v", name, id, ok, c.want, c.ok)
		}
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
	var before, after runtimeMem
	before.read()
	if err := b.readStdoutPipe(bytes.NewReader(over), testCap); err != nil {
		t.Fatal(err)
	}
	after.read()
	if grown := after.total - before.total; grown > 4*testCap+1<<20 {
		t.Errorf("allocated %d bytes reading a 20 MB line with a %d byte cap", grown, testCap)
	}
}
