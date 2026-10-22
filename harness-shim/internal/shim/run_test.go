package shim

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	_ = BecomeSubreaper()
	os.Exit(m.Run())
}

func runTurn(t *testing.T, cfg Config, turn Turn) (string, error) {
	t.Helper()
	cfg.Output = OutputText
	return NewRunner(cfg, testEnv).Run(context.Background(), turn)
}

func TestThePromptReachesTheCommandInAFileAndNeverInArgv(t *testing.T) {
	prompts := map[string]string{
		"leading dash": "-rf --help",
		"metachars":    "$(touch /tmp/x); `id` | cat > /dev/null && 'q' \"q\"\n*",
		"large":        strings.Repeat("a", 200<<10),
	}
	for name, q := range prompts {
		t.Run(name, func(t *testing.T) {
			// $# is 1: only the file name is an argument.
			cfg := Config{Argv: []string{binSh, "-c", `test "$#" = 1 && /bin/cat "$1"`, "sh", promptTag}}
			got, err := runTurn(t, cfg, Turn{Question: q})
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(got) != strings.TrimSpace(q) {
				t.Fatalf("prompt changed: got %d bytes, want %d", len(got), len(q))
			}
		})
	}
}

func TestTheCommandSeesOnlyTheAllowlistedEnvironment(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "leak")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	cfg := Config{Argv: sh(`env`)}
	got, err := runTurn(t, cfg, Turn{Question: "q", TurnKey: "k", TimeZone: "America/New_York"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "leak") || strings.Contains(got, "KUBERNETES_SERVICE_HOST") {
		t.Fatalf("the shim's environment leaked:\n%s", got)
	}
	for _, want := range []string{"PATH=", "HOME=", "KUBEMOOT_TURN_KEY=k", "OPENAI_API_KEY=k", "TZ=America/New_York", "KUBEMOOT_MODEL_URL=", "KUBEMOOT_TOOLS_URL="} {
		if !strings.Contains(got, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
}

func TestCapturedOutputIsCappedAndMarked(t *testing.T) {
	out := &capWriter{max: 10}
	n, err := out.Write([]byte(strings.Repeat("x", 25)))
	if n != 25 || err != nil || out.String() != strings.Repeat("x", 10)+truncMark {
		t.Fatalf("head cap: n=%d err=%v %q", n, err, out.String())
	}
	tail := &capWriter{max: 4, keepTail: true}
	_, _ = tail.Write([]byte("abcdef"))
	_, _ = tail.Write([]byte("gh"))
	if tail.String() != "[output truncated]\nefgh" {
		t.Fatalf("tail cap: %q", tail.String())
	}
	small := &capWriter{max: 10}
	_, _ = small.Write([]byte("ok"))
	if small.String() != "ok" {
		t.Fatalf("small: %q", small.String())
	}
}

func TestACommandPrintingMoreThanTheCapStillFinishes(t *testing.T) {
	cfg := Config{Argv: sh(`head -c 3000000 /dev/zero | tr '\0' a`)}
	got, err := runTurn(t, cfg, Turn{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxCapture+len(truncMark) || !strings.HasSuffix(got, "[output truncated]") {
		t.Fatalf("got %d bytes", len(got))
	}
}

func TestAnOrphanedDescendantIsKilledAndReapedWhenTheCommandExits(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pid")
	cfg := Config{Argv: sh(`sleep 60 >/dev/null 2>&1 & echo $! > ` + pids + `; echo done`)}
	start := time.Now()
	if _, err := runTurn(t, cfg, Turn{Question: "q"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	b, _ := os.ReadFile(pids)
	pid := strings.TrimSpace(string(b))
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatal(err)
	}
	// A reaped process has no /proc entry; a zombie would still have one.
	if _, err := os.Stat("/proc/" + pid); err == nil {
		t.Fatalf("process %s is still in the process table", pid)
	}
}

// Hostile request values reach the environment only; the command line holds none of them.
func TestRequestValuesReachTheEnvironmentAndNeverArgv(t *testing.T) {
	turn := Turn{Question: "q;id", Instructions: "$(id)", TurnKey: "k;touch x", TimeZone: "; rm -rf /", Now: "`id`"}
	cfg := Config{
		Argv: []string{"/bin/goose", "--tools", "{{toolsURL}}", "-i", "{{promptFile}}"},
		Env:  []string{"K={{turnKey}}", "Z={{timeZone}}", "N={{now}}"},
	}
	r := NewRunner(cfg, testEnv)
	argv := r.Argv("/h/prompt.txt")
	for _, a := range argv {
		for _, v := range []string{turn.Question, turn.Instructions, turn.TurnKey, turn.TimeZone, turn.Now} {
			if strings.Contains(a, v) {
				t.Errorf("argv element %q carries %q", a, v)
			}
		}
	}
	env := r.Environ(turn, "/h")
	for _, want := range []string{"K=k;touch x", "Z=; rm -rf /", "N=`id`"} {
		if !slices.Contains(env, want) {
			t.Errorf("environment lacks %q", want)
		}
	}
}
