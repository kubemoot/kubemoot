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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	// Self is the sandbox binary that confines the program; empty means this executable.
	Self string
}

// Option adjusts one run.
type Option func(*job)

type job struct {
	inputs []inputs.Input
	thread string
}

// WithInputs stages stored results as files under inputs/ in the program's directory.
func WithInputs(in []inputs.Input) Option { return func(j *job) { j.inputs = in } }

// WithThread names the discussion the program runs for, as "{namespace}/{crew}/{thread}/". Its folder
// of the workspace is what the program sees in place of the whole workspace.
func WithThread(prefix string) Option { return func(j *job) { j.thread = prefix } }

// Execute runs code in the given language.
func (r *Runner) Execute(ctx context.Context, language, code string, opts ...Option) (Result, error) {
	var j job
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
	if err := stage(dir, j.inputs); err != nil {
		return Result{}, err
	}
	cmdline := append(append([]string{}, argv(lang)...), file)
	attr := &syscall.SysProcAttr{Setpgid: true}
	if mounts := r.mounts(j); len(mounts) > 0 {
		cmdline = confine.Command(r.self(), mounts, cmdline)
		attr = confine.Attr()
	}
	res, err := r.run(ctx, cmdline, dir, attr)
	if err != nil && attr.Cloneflags != 0 {
		err = fmt.Errorf("the program could not start with the workspace hidden (this needs user namespaces: "+
			"run the sandbox under a sandboxed runtime such as gVisor, or set SANDBOX_CONFINEMENT=off): %w", err)
	}
	return res, err
}

// mounts are the views that replace the workspace directories that exist; none when confinement is off.
func (r *Runner) mounts(j job) []confine.Mount {
	if r.Unconfined {
		return nil
	}
	var out []confine.Mount
	for _, dir := range r.WorkspaceDirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, confine.Mount{Path: dir, View: threadView(dir, j.thread)})
	}
	return out
}

// threadView is the discussion's folder under dir, or empty when there is no such folder.
func threadView(dir, thread string) string {
	if thread == "" || !filepath.IsLocal(thread) {
		return ""
	}
	view := filepath.Join(dir, thread)
	if info, err := os.Stat(view); err != nil || !info.IsDir() {
		return ""
	}
	return view
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

// stage copies each input to inputs/<name> in the run directory, which the program reads as a file.
func stage(dir string, in []inputs.Input) error {
	for _, i := range in {
		if !filepath.IsLocal(i.Name) {
			return fmt.Errorf("input %q is not a plain name", i.Name)
		}
		dest := filepath.Join(dir, "inputs", i.Name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		if err := copyFile(i.Source, dest); err != nil {
			return fmt.Errorf("input %q: %w", i.Name, err)
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	from, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = from.Close() }()
	to, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(to, from); err != nil {
		_ = to.Close()
		return err
	}
	return to.Close()
}

func (r *Runner) run(ctx context.Context, argv []string, dir string, attr *syscall.SysProcAttr) (Result, error) {
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
