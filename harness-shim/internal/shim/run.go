package shim

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// killGrace is how long Wait lets the command's output pipes drain after the process is killed.
const killGrace = 2 * time.Second

const (
	// promptFileName is the turn's prompt, written to the private turn directory.
	promptFileName = "prompt.txt"
	// maxCapture caps each of the command's captured output streams.
	maxCapture = 1 << 20
	truncMark  = "\n[output truncated]"
)

// Runner runs the configured command once per turn.
type Runner struct {
	cfg Config
	env Environment
}

// NewRunner returns a Runner for a validated configuration.
func NewRunner(cfg Config, env Environment) *Runner { return &Runner{cfg: cfg, env: env} }

// shimPairs are the placeholders whose values come from the shim's own environment and the turn's
// private directory. They are the only ones an argv element may use.
func (r *Runner) shimPairs(promptFile string) []string {
	return []string{
		"{{promptFile}}", promptFile,
		"{{modelURL}}", r.env.ModelURL, "{{modelHost}}", strings.TrimSuffix(strings.TrimSuffix(r.env.ModelURL, "/"), "/v1"),
		"{{toolsURL}}", r.env.ToolsURL, "{{nativeTools}}", r.env.NativeTools,
	}
}

// envReplacer also expands the request-derived placeholders, which only env entries may use.
func (r *Runner) envReplacer(t Turn, promptFile string) *strings.Replacer {
	return strings.NewReplacer(append(r.shimPairs(promptFile),
		"{{prompt}}", t.Prompt(), "{{question}}", t.Question, "{{instructions}}", t.Instructions,
		"{{turnKey}}", t.TurnKey, "{{timeZone}}", t.TimeZone, "{{now}}", t.Now,
	)...)
}

// Argv expands the configured command with a prompt file path. It sees no turn: the command line
// is built only from configuration and shim-controlled values. A leading [mode,mode] keeps the
// element only when the native-tools mode is one of them. {{promptFile}} is the file holding the
// turn's prompt, so the prompt never travels in the process list.
func (r *Runner) Argv(promptFile string) []string {
	repl := strings.NewReplacer(r.shimPairs(promptFile)...)
	out := make([]string, 0, len(r.cfg.Argv))
	for _, a := range r.cfg.Argv {
		if kept, rest := r.applyCondition(a); kept {
			out = append(out, repl.Replace(rest))
		}
	}
	return out
}

func (r *Runner) applyCondition(a string) (bool, string) {
	if !strings.HasPrefix(a, "[") {
		return true, a
	}
	end := strings.Index(a, "]")
	if end < 0 {
		return true, a
	}
	for _, m := range strings.Split(a[1:end], ",") {
		if strings.TrimSpace(m) == r.env.NativeTools {
			return true, a[end+1:]
		}
	}
	return false, ""
}

// Environ is the command's environment, built from an allowlist: PATH, LANG and TZ from the shim's
// own, HOME as the private turn directory, the Moot Mesh variables for this turn, then the
// configured entries. Nothing else of the shim's environment reaches the command.
func (r *Runner) Environ(t Turn, home string) []string {
	repl := r.envReplacer(t, filepath.Join(home, promptFileName))
	env := []string{
		"HOME=" + home,
		"KUBEMOOT_MODEL_URL=" + r.env.ModelURL, "KUBEMOOT_TOOLS_URL=" + r.env.ToolsURL,
		"KUBEMOOT_TURN_KEY=" + t.TurnKey, "OPENAI_API_KEY=" + t.TurnKey,
		"KUBEMOOT_TIME_ZONE=" + t.TimeZone, "KUBEMOOT_NOW=" + t.Now,
		"KUBEMOOT_NATIVE_TOOLS=" + r.env.NativeTools,
	}
	for _, k := range []string{"PATH", "LANG"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if tz := cmp.Or(t.TimeZone, os.Getenv("TZ")); tz != "" {
		env = append(env, "TZ="+tz)
	}
	for _, e := range r.cfg.Env {
		env = append(env, repl.Replace(e))
	}
	return env
}

// Run runs the command for the turn and returns its answer. ctx ends the command: the process
// group is killed, so a harness that started children leaves none behind.
func (r *Runner) Run(ctx context.Context, t Turn) (string, error) {
	home, err := os.MkdirTemp("", "turn-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(home) }()
	promptFile := filepath.Join(home, promptFileName)
	if err := os.WriteFile(promptFile, []byte(t.Prompt()), 0o600); err != nil {
		return "", err
	}
	argv := r.Argv(promptFile)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = r.Environ(t, home)
	cmd.Dir = home
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Cancel runs only after Start succeeded, so cmd.Process is set; Setpgid makes the child's PID its group's ID.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = killGrace
	stdout, stderr := &capWriter{max: maxCapture}, &capWriter{max: maxCapture, keepTail: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Start()
	if err == nil {
		pgid := cmd.Process.Pid
		err = cmd.Wait()
		reapGroup(pgid)
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", context.Cause(ctx)
		}
		return "", fmt.Errorf("%s: %w: %s", argv[0], err, tail(stderr.String()))
	}
	return Extract(r.cfg.Output, stdout.String())
}

// tail keeps the end of the command's stderr, where the reason is.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1000 {
		return "..." + s[len(s)-1000:]
	}
	return s
}

// capWriter keeps at most max bytes of what is written. It always accepts the write, so a chatty
// command never blocks on a full pipe. By default the first max bytes stay; with keepTail the last.
type capWriter struct {
	buf       []byte
	max       int
	keepTail  bool
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.keepTail {
		w.buf = append(w.buf, p...)
		if len(w.buf) > w.max {
			w.buf, w.truncated = append([]byte(nil), w.buf[len(w.buf)-w.max:]...), true
		}
		return n, nil
	}
	if room := w.max - len(w.buf); len(p) > room {
		p, w.truncated = p[:room], true
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

// String is the kept output, marked when bytes were dropped.
func (w *capWriter) String() string {
	if !w.truncated {
		return string(w.buf)
	}
	if w.keepTail {
		return "[output truncated]\n" + string(w.buf)
	}
	return string(w.buf) + truncMark
}

// reapGroup kills what the finished command left in its process group and reaps it. The shim is
// PID 1, so orphaned descendants reparent to it; waiting on the group (never on -1) cannot take
// another turn's child or the exit status that cmd.Wait already collected.
func reapGroup(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	for range 100 {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-pgid, &ws, syscall.WNOHANG, nil)
		if err == syscall.EINTR || pid > 0 {
			continue
		}
		if err != nil { // ECHILD: no member of the group is left
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// prSetChildSubreaper is PR_SET_CHILD_SUBREAPER from <linux/prctl.h>.
const prSetChildSubreaper = 36

// BecomeSubreaper makes orphaned descendants of the commands reparent to this process even when
// it is not PID 1, so reapGroup can reap them.
func BecomeSubreaper() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		return errno
	}
	return nil
}
