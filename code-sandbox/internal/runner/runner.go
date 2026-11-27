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
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/confine"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/inputs"
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
	// WorkspaceDirs are the workspace volume's mount paths. A directory that exists is hidden from
	// the program: it sees the calling discussion's own folder there, or an empty one.
	WorkspaceDirs []string
	// Unconfined leaves the workspace visible to the program. It is for a pod without the
	// namespaces confinement needs; the program then reads every discussion's files.
	Unconfined bool
	// TempSize is the size limit of the private temporary directory of each run, in tmpfs syntax
	// (256m); empty means DefaultTempSize. The memory it uses counts against the pod's limit.
	TempSize string
	// RequireProc refuses to run a program whose /proc cannot be replaced; otherwise the run goes on and
	// the server logs it once.
	RequireProc bool
	// Logf reports what the helpers tell the server; nil means the standard logger.
	Logf func(format string, args ...any)
	// Self is the sandbox binary that confines the program; empty means this executable.
	Self string

	procOnce    sync.Once
	beforeStart func() // a test hook, called after the helper's command is built and before it starts
}

// Option adjusts one run.
type Option func(*job)

type job struct {
	inputs  []inputs.Input
	thread  string
	confine bool
}

// WithInputs stages stored results as files under inputs/ in the program's directory.
func WithInputs(in []inputs.Input) Option { return func(j *job) { j.inputs = in } }

// WithThread names the discussion the program runs for, as "{namespace}/{crew}/{thread}/". Its folder
// of the workspace is what the program sees in place of the whole workspace.
func WithThread(prefix string) Option { return func(j *job) { j.thread = prefix } }

// Execute runs code in the given language.
func (r *Runner) Execute(ctx context.Context, language, code string, opts ...Option) (Result, error) {
	j := job{confine: true}
	for _, o := range opts {
		o(&j)
	}
	return r.withSource(ctx, language, code, func(l Language) []string { return l.Run }, j)
}

// Validate syntax-checks code in the given language without running it.
func (r *Runner) Validate(ctx context.Context, language, code string) (Result, error) {
	return r.withSource(ctx, language, code, func(l Language) []string { return l.Check }, job{})
}

func (r *Runner) withSource(
	ctx context.Context, language, code string, argv func(Language) []string, j job,
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
	if err := stage(ctx, dir, j.inputs); err != nil {
		return Result{}, err
	}
	cmdline := append(append([]string{}, argv(lang)...), file)
	if r.Unconfined || !j.confine {
		return r.run(ctx, cmdline, dir, &syscall.SysProcAttr{Setpgid: true}, nil)
	}
	return r.runConfined(ctx, cmdline, dir, j)
}

// runConfined runs cmdline through the confining helper.
func (r *Runner) runConfined(ctx context.Context, cmdline []string, dir string, j job) (Result, error) {
	mounts, err := r.mounts(j)
	if err != nil {
		return Result{}, err
	}
	status, err := newStatusPipe()
	if err != nil {
		return Result{}, err
	}
	defer status.close()
	spec := confine.Spec{Mounts: mounts, RunDir: dir, TempSize: r.tempSize(),
		RequireProc: r.RequireProc, StatusFD: firstExtraFD}
	cmdline = confine.Command(r.self(), spec, cmdline)
	if r.beforeStart != nil {
		r.beforeStart()
	}
	res, err := r.run(ctx, cmdline, dir, confine.Attr(), []*os.File{status.w})
	r.noteProc(status.report())
	return res, confinementError(res, err)
}

// firstExtraFD is the descriptor number of the first extra file a child receives.
const firstExtraFD = 3

// statusPipe carries the helper's reports to the server.
type statusPipe struct{ r, w *os.File }

func newStatusPipe() (*statusPipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &statusPipe{r: r, w: w}, nil
}

func (p *statusPipe) close() {
	_ = p.r.Close()
	_ = p.w.Close()
}

// report is what the helper wrote, read once the run has ended and the parent's copy of the write end is closed.
func (p *statusPipe) report() string {
	_ = p.w.Close()
	_ = p.r.SetReadDeadline(time.Now().Add(time.Second))
	b, _ := io.ReadAll(io.LimitReader(p.r, 4096))
	return strings.TrimSpace(string(b))
}

// noteProc logs, once, that a run could not get its own /proc.
func (r *Runner) noteProc(report string) {
	if !strings.HasPrefix(report, confine.ProcNotReplaced) {
		return
	}
	r.procOnce.Do(func() {
		logf := r.Logf
		if logf == nil {
			logf = log.Printf
		}
		logf("code-sandbox: programs keep the host's /proc (%s); the kernel's ptrace rules still deny other "+
			"processes, and SANDBOX_REQUIRE_PROC=1 refuses to run instead", report)
	})
}

// confinementError turns a helper that could not set the view up, or a program that could not start in
// the new namespaces, into a tool error rather than the program's own result.
func confinementError(res Result, err error) error {
	const hint = "this needs user namespaces and mounts: run the sandbox under a sandboxed runtime such as gVisor, " +
		"or set SANDBOX_CONFINEMENT=off (which leaves every discussion's files readable to programs)"
	switch {
	case err != nil:
		return fmt.Errorf("the program could not start with the workspace hidden (%s): %w", hint, err)
	case res.ExitCode == confine.ExitCode && strings.HasPrefix(res.Stderr, confine.FailurePrefix):
		return fmt.Errorf("%s (%s)", strings.TrimSpace(res.Stderr), hint)
	}
	return nil
}

// mounts are the views that replace the workspace directories that exist. A directory that cannot be
// examined for a reason other than being absent stops the run: the program must not start with it visible.
// The helper opens the discussion's folder itself, so nothing is resolved here.
func (r *Runner) mounts(j job) ([]confine.Mount, error) {
	var out []confine.Mount
	for _, dir := range r.WorkspaceDirs {
		info, err := os.Stat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("the workspace %s cannot be examined, so it cannot be hidden: %w", dir, err)
		case !info.IsDir():
			continue
		}
		out = append(out, confine.Mount{Path: dir, Thread: j.thread})
	}
	return out, nil
}

// DefaultTempSize is the size of the private temporary directory a program gets.
const DefaultTempSize = "256m"

func (r *Runner) tempSize() string {
	if r.TempSize != "" {
		return r.TempSize
	}
	return DefaultTempSize
}

func (r *Runner) self() string {
	if r.Self != "" {
		return r.Self
	}
	self, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return self
}

// MaxInputBytes and MaxTotalBytes cap one staged input and all the inputs of one run.
const (
	MaxInputBytes = 64 << 20
	MaxTotalBytes = 256 << 20
)

// stage copies each input to inputs/<name> in the run directory, which the program reads as a file.
func stage(ctx context.Context, dir string, in []inputs.Input) error {
	budget := int64(MaxTotalBytes)
	for _, i := range in {
		if !filepath.IsLocal(i.Name) {
			return fmt.Errorf("input %q is not a plain name", i.Name)
		}
		dest := filepath.Join(dir, "inputs", i.Name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		n, err := copyFile(ctx, i.File, dest, min(int64(MaxInputBytes), budget))
		if err != nil {
			return fmt.Errorf("input %q: %w", i.Name, err)
		}
		budget -= n
	}
	return nil
}

// copyFile copies an input that was opened and checked as a regular file, and fails past limit bytes or
// when ctx ends. The destination is never a link.
func copyFile(ctx context.Context, from *os.File, dest string, limit int64) (int64, error) {
	info, err := from.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("not a regular file")
	}
	if info.Size() > limit {
		return 0, fmt.Errorf("is %d bytes; the limit is %d (per input %d, all inputs %d)",
			info.Size(), limit, MaxInputBytes, MaxTotalBytes)
	}
	to, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(to, io.LimitReader(ctxReader{ctx, from}, limit+1))
	if cerr := to.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("grew past the limit of %d bytes", limit)
	}
	return n, err
}

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func (r *Runner) run(
	ctx context.Context, argv []string, dir string, attr *syscall.SysProcAttr, extra []*os.File,
) (Result, error) {
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
	cmd.SysProcAttr = attr
	cmd.ExtraFiles = extra
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
	env := []string{
		"PATH=" + path,
		"HOME=" + home,
		"LANG=C.UTF-8",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONUNBUFFERED=1",
	}
	// An interpreter built as a shared library finds libpython through this.
	if libs := os.Getenv("LD_LIBRARY_PATH"); libs != "" {
		env = append(env, "LD_LIBRARY_PATH="+libs)
	}
	return env
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
