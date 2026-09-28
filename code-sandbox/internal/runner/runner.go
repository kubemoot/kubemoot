// Package runner executes a short program in a fresh temporary directory and
// returns its output. Each run gets no stdin (a read returns end of input at
// once), a HOME inside the temporary directory, a bounded amount of captured
// output, and a time limit after which the program's whole process group is
// killed, so nothing it started outlives the call.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// Language says how to run and how to syntax-check a source file.
type Language struct {
	Ext   string
	Run   []string
	Check []string
}

const bash = "bash"

// pythonCheck compiles the file without running it or writing bytecode.
const pythonCheck = "import sys; compile(open(sys.argv[1]).read(), sys.argv[1], 'exec')"

// Languages are the languages the sandbox runs.
var Languages = map[string]Language{
	"python": {Ext: ".py", Run: []string{"python3"}, Check: []string{"python3", "-c", pythonCheck}},
	bash:     {Ext: ".sh", Run: []string{bash}, Check: []string{bash, "-n"}},
}

// Names lists the supported languages in a stable order.
func Names() []string {
	names := make([]string, 0, len(Languages))
	for name := range Languages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Result is what one run produced.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
}

// Runner holds the limits every run shares.
type Runner struct {
	Timeout   time.Duration
	MaxOutput int
	// TempDir is where each run's directory is created; empty means the OS default.
	TempDir string
}

// Execute runs code in the given language.
func (r *Runner) Execute(ctx context.Context, language, code string) (Result, error) {
	return r.withSource(ctx, language, code, func(l Language) []string { return l.Run })
}

// Validate syntax-checks code in the given language without running it.
func (r *Runner) Validate(ctx context.Context, language, code string) (Result, error) {
	return r.withSource(ctx, language, code, func(l Language) []string { return l.Check })
}

func (r *Runner) withSource(
	ctx context.Context, language, code string, argv func(Language) []string,
) (Result, error) {
	lang, ok := Languages[language]
	if !ok {
		return Result{}, fmt.Errorf("unsupported language %q; use one of %v", language, Names())
	}
	dir, err := os.MkdirTemp(r.TempDir, "code-sandbox-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "main"+lang.Ext)
	if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
		return Result{}, err
	}
	return r.run(ctx, append(append([]string{}, argv(lang)...), file), dir)
}

func (r *Runner) run(ctx context.Context, argv []string, dir string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	stdout := &cappedBuffer{max: r.MaxOutput}
	stderr := &cappedBuffer{max: r.MaxOutput}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = environment(dir)
	cmd.Stdin = nil // os/exec connects the null device: a read sees end of input
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	// A background child that keeps stdout open gets this long before the pipes close.
	cmd.WaitDelay = 250 * time.Millisecond

	start := time.Now()
	err := cmd.Run()
	_ = killGroup(cmd) // background children of a finished program
	res := Result{
		ExitCode:  exitCode(cmd),
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Duration:  time.Since(start),
		TimedOut:  errors.Is(ctx.Err(), context.DeadlineExceeded),
		Truncated: stdout.truncated || stderr.truncated,
	}
	return exitStatus(res, err)
}

// exitStatus fills in the exit code. A program that ran and failed is a result,
// not an error; only a program that could not start is an error.
func exitStatus(res Result, err error) (Result, error) {
	var exitErr *exec.ExitError
	switch {
	case err == nil, errors.Is(err, exec.ErrWaitDelay):
		return res, nil
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	case res.TimedOut:
		res.ExitCode = -1
		return res, nil
	default:
		return res, err
	}
}

func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return 0
	}
	return cmd.ProcessState.ExitCode()
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func environment(home string) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	return []string{
		"PATH=" + path,
		"HOME=" + home,
		"LANG=C.UTF-8",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONUNBUFFERED=1",
	}
}

// cappedBuffer keeps the first max bytes written and notes that more arrived.
type cappedBuffer struct {
	buf       []byte
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - len(b.buf)
	if b.max <= 0 || len(p) <= room {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	if room > 0 {
		b.buf = append(b.buf, p[:room]...)
	}
	b.truncated = true
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }
