package a2a

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
)

// fakeGoose prints what `goose run --output-format json` prints for one answered turn, and
// echoes the environment the shim gave it so the test can see it arrived.
const fakeGoose = `#!/bin/sh
case "$*" in *--no-profile*) profile=none;; *) profile=default;; esac
printf '{"messages":[{"role":"user","content":[{"type":"text","text":"q"}]},{"role":"assistant","content":[{"type":"text","text":"answer profile=%s key=%s tz=%s model=%s"}]}],"metadata":{"status":"completed"}}' \
  "$profile" "$OPENAI_API_KEY" "$KUBEMOOT_TIME_ZONE" "$OPENAI_HOST"
`

// startShim builds the harness shim and runs it with a fake goose, on a free port.
func startShim(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the harness shim")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "harness-shim")
	build := exec.Command("go", "build", "-o", bin, "./cmd/harness-shim")
	build.Dir = "../../../harness-shim"
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the shim: %v\n%s", err, out)
	}
	script := filepath.Join(dir, "goose")
	if err := os.WriteFile(script, []byte(fakeGoose), 0o700); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	cfg := map[string]any{
		"argv":   []string{script, "run", "[none]--no-profile", "-t", "{{prompt}}"},
		"env":    []string{"OPENAI_HOST={{modelHost}}", "OPENAI_API_KEY={{turnKey}}"},
		"output": "goose-json",
	}
	b, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(dir, "goose.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, "-config", cfgPath)
	cmd.Env = append(os.Environ(), "KUBEMOOT_TURN_PORT="+itoa(port), "KUBEMOOT_MODEL_URL=http://127.0.0.1:8081/v1",
		"KUBEMOOT_TOOLS_URL=http://127.0.0.1:8082/mcp", "KUBEMOOT_NATIVE_TOOLS=none")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	root := "http://127.0.0.1:" + itoa(port)
	waitForCard(t, root)
	return root
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func waitForCard(t *testing.T, root string) {
	t.Helper()
	for range 100 {
		if Alive(context.Background(), root, http.DefaultClient) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the shim never served its agent card")
}

// The harness shim with a fake goose passes the sidecar's own client: the card is found and
// accepted, a turn is answered, and the answer reads as an agreement.
func TestTheHarnessShimConformsToTheSidecarsTurnClient(t *testing.T) {
	root := startShim(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Discover(ctx, root, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	msg := NewTurn("thread-1", "What is the date?", TurnInput{
		Kind: "discussion", Agent: "goose-seat", Crew: "pilot", Phase: PhaseEvaluating,
		TurnKey: "turn-key-1", DeadlineSeconds: 20, TimeZone: "America/New_York", Now: "2026-10-22T12:00:00Z"})
	r, err := c.Do(ctx, msg, nil)
	out := Interpret(r, err)
	if out.Signal != SignalAgree {
		t.Fatalf("signal = %s (%s %s)", out.Signal, out.FailureType, out.Reason)
	}
	want := "answer profile=none key=turn-key-1 tz=America/New_York model=http://127.0.0.1:8081"
	if out.Content != want {
		t.Fatalf("content = %q, want %q", out.Content, want)
	}
	if r.State != sdk.TaskStateCompleted {
		t.Fatalf("state = %v", r.State)
	}
}

// A turn whose command fails reaches the sidecar as a failure, never as silence.
func TestTheHarnessShimReportsAFailedTurnAsAFailure(t *testing.T) {
	root := startShim(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Discover(ctx, root, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	// An empty question is refused by the shim with a 400, which the client returns as an error.
	r, err := c.Do(ctx, NewTurn("thread-2", "", TurnInput{TurnKey: "k"}), nil)
	if out := Interpret(r, err); out.Signal != SignalFailure {
		t.Fatalf("signal = %s", out.Signal)
	}
}
