package shim

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// killGrace is how long Wait lets the command's output pipes drain after the process is killed.
const killGrace = 2 * time.Second

// Runner runs the configured command once per turn.
type Runner struct {
	cfg Config
	env Environment
}

// NewRunner returns a Runner for a validated configuration.
func NewRunner(cfg Config, env Environment) *Runner { return &Runner{cfg: cfg, env: env} }

func (r *Runner) replacer(t Turn) *strings.Replacer {
	return strings.NewReplacer(
		"{{prompt}}", t.Prompt(), "{{question}}", t.Question, "{{instructions}}", t.Instructions,
		"{{modelURL}}", r.env.ModelURL, "{{modelHost}}", strings.TrimSuffix(strings.TrimSuffix(r.env.ModelURL, "/"), "/v1"),
		"{{toolsURL}}", r.env.ToolsURL, "{{turnKey}}", t.TurnKey, "{{timeZone}}", t.TimeZone,
		"{{now}}", t.Now, "{{nativeTools}}", r.env.NativeTools,
	)
}

// Argv expands the configured command for a turn. A leading [mode,mode] keeps the element only
// when the native-tools mode is one of them.
func (r *Runner) Argv(t Turn) []string {
	repl := r.replacer(t)
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

// Environ is the command's environment: the shim's own, then the Moot Mesh variables for this turn,
// then the configured entries. HOME is a private directory so no turn sees another's state.
func (r *Runner) Environ(t Turn, home string) []string {
	repl := r.replacer(t)
	env := append(os.Environ(),
		"KUBEMOOT_MODEL_URL="+r.env.ModelURL, "KUBEMOOT_TOOLS_URL="+r.env.ToolsURL,
		"KUBEMOOT_TURN_KEY="+t.TurnKey, "KUBEMOOT_TIME_ZONE="+t.TimeZone, "KUBEMOOT_NOW="+t.Now,
		"KUBEMOOT_NATIVE_TOOLS="+r.env.NativeTools, "HOME="+home)
	if t.TimeZone != "" {
		env = append(env, "TZ="+t.TimeZone)
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
	argv := r.Argv(t)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the command is the operator's configuration
	cmd.Env = r.Environ(t, home)
	cmd.Dir = home
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = killGrace
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
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
