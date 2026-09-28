package runner

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func newRunner(t *testing.T) *Runner {
	t.Helper()
	return &Runner{Timeout: 10 * time.Second, MaxOutput: 64 * 1024, TempDir: t.TempDir()}
}

func execute(t *testing.T, r *Runner, language, code string) Result {
	t.Helper()
	res, err := r.Execute(context.Background(), language, code)
	if err != nil {
		t.Fatalf("Execute(%s): %v", language, err)
	}
	return res
}

func TestPythonPrints(t *testing.T) {
	res := execute(t, newRunner(t), "python", "print(sum(range(10)))")
	if strings.TrimSpace(res.Stdout) != "45" || res.ExitCode != 0 {
		t.Fatalf("got stdout %q exit %d", res.Stdout, res.ExitCode)
	}
}

func TestBashPrints(t *testing.T) {
	res := execute(t, newRunner(t), "bash", "echo $((6 * 7))")
	if strings.TrimSpace(res.Stdout) != "42" || res.ExitCode != 0 {
		t.Fatalf("got stdout %q exit %d", res.Stdout, res.ExitCode)
	}
}

func TestStdinIsAtEndOfInput(t *testing.T) {
	r := newRunner(t)
	r.Timeout = 5 * time.Second
	res := execute(t, r, "python", "import sys\nprint(len(sys.stdin.read()))")
	if res.TimedOut || strings.TrimSpace(res.Stdout) != "0" {
		t.Fatalf("a stdin read should end at once: stdout %q timedOut %v", res.Stdout, res.TimedOut)
	}
	res = execute(t, r, "bash", "cat; echo done")
	if res.TimedOut || strings.TrimSpace(res.Stdout) != "done" {
		t.Fatalf("bash cat should end at once: stdout %q timedOut %v", res.Stdout, res.TimedOut)
	}
}

func TestTimeoutStopsTheProgram(t *testing.T) {
	r := newRunner(t)
	r.Timeout = 500 * time.Millisecond
	res := execute(t, r, "python", "import time\nprint('started', flush=True)\ntime.sleep(30)")
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("want timed out with exit -1, got %+v", res)
	}
	if res.Duration > 5*time.Second {
		t.Fatalf("the program was not stopped promptly: %v", res.Duration)
	}
	if !strings.Contains(res.Stdout, "started") {
		t.Fatalf("output before the timeout is kept, got %q", res.Stdout)
	}
}

func TestBackgroundChildrenDoNotHoldTheCall(t *testing.T) {
	r := newRunner(t)
	res := execute(t, r, "bash", "sleep 30 &\necho started")
	if res.TimedOut || res.Duration > 5*time.Second {
		t.Fatalf("a background child held the call: %+v", res)
	}
	if strings.TrimSpace(res.Stdout) != "started" {
		t.Fatalf("got stdout %q", res.Stdout)
	}
}

func TestFailureIsAResultNotAnError(t *testing.T) {
	res := execute(t, newRunner(t), "python", "import sys\nprint('oops', file=sys.stderr)\nsys.exit(3)")
	if res.ExitCode != 3 || !strings.Contains(res.Stderr, "oops") {
		t.Fatalf("got exit %d stderr %q", res.ExitCode, res.Stderr)
	}
	res = execute(t, newRunner(t), "python", "raise ValueError('bad input')")
	if res.ExitCode != 1 || !strings.Contains(res.Stderr, "ValueError: bad input") {
		t.Fatalf("got exit %d stderr %q", res.ExitCode, res.Stderr)
	}
}

func TestUnsupportedLanguage(t *testing.T) {
	_, err := newRunner(t).Execute(context.Background(), "cobol", "DISPLAY 'HI'.")
	if err == nil || !strings.Contains(err.Error(), "unsupported language") {
		t.Fatalf("want unsupported language error, got %v", err)
	}
}

func TestOutputIsCapped(t *testing.T) {
	r := newRunner(t)
	r.MaxOutput = 100
	res := execute(t, r, "python", "print('x' * 10000)")
	if len(res.Stdout) != 100 || !res.Truncated {
		t.Fatalf("want 100 bytes and truncated, got %d bytes truncated %v", len(res.Stdout), res.Truncated)
	}
}

func TestHomeAndWorkingDirectoryAreTheRunDirectory(t *testing.T) {
	r := newRunner(t)
	res := execute(t, r, "bash", "echo \"$HOME\"; pwd")
	lines := strings.Fields(res.Stdout)
	if len(lines) != 2 || lines[0] != lines[1] || !strings.HasPrefix(lines[0], r.TempDir) {
		t.Fatalf("want HOME == pwd under %s, got %q", r.TempDir, res.Stdout)
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("the run directory should be removed after the call, stat err %v", err)
	}
}

func TestProgramsCanReadFilesByPath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/data.txt"
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := execute(t, newRunner(t), "python", "print(len(open('"+path+"').read().splitlines()))")
	if strings.TrimSpace(res.Stdout) != "3" {
		t.Fatalf("got %q stderr %q", res.Stdout, res.Stderr)
	}
}

func TestValidate(t *testing.T) {
	r := newRunner(t)
	cases := []struct {
		language, code string
		valid          bool
	}{
		{"python", "print('ok')", true},
		{"python", "def f(:\n  pass", false},
		{"bash", "echo ok", true},
		{"bash", "if true; then echo", false},
	}
	for _, c := range cases {
		res, err := r.Validate(context.Background(), c.language, c.code)
		if err != nil {
			t.Fatalf("Validate(%s): %v", c.language, err)
		}
		if (res.ExitCode == 0) != c.valid {
			t.Errorf("%s %q: valid=%v, got exit %d stderr %q", c.language, c.code, c.valid, res.ExitCode, res.Stderr)
		}
	}
}

func TestValidateDoesNotRunTheCode(t *testing.T) {
	r := newRunner(t)
	marker := t.TempDir() + "/ran"
	res, err := r.Validate(context.Background(), "python", "open('"+marker+"', 'w').write('x')")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Validate: %v %+v", err, res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Validate ran the program")
	}
}

func TestNamesAreSorted(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "bash,python" {
		t.Fatalf("got %s", got)
	}
}

func TestCappedBufferUnlimited(t *testing.T) {
	b := &cappedBuffer{}
	n, _ := b.Write([]byte("hello"))
	if n != 5 || b.String() != "hello" || b.truncated {
		t.Fatalf("got %d %q %v", n, b.String(), b.truncated)
	}
}
