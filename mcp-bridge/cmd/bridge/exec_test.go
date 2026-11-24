package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/mcp-bridge/internal/pipetest"
)

// TestHelperRunExec is not a test: the tests below run the test binary again with
// HELPER_EXEC_ARGS set, and this function runs runExec in that child process,
// which replaces itself with the requested command.
func TestHelperRunExec(t *testing.T) {
	args, ok := os.LookupEnv("HELPER_EXEC_ARGS")
	if !ok {
		t.Skip("runs only as a child of the exec tests")
	}
	runExec(strings.Split(args, "\x1f"))
}

// helper builds the child process; the arguments reach runExec as given, so one
// may contain spaces.
func helper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRunExec$")
	cmd.Env = append(os.Environ(), "HELPER_EXEC_ARGS="+strings.Join(args, "\x1f"))
	return cmd
}

func makePipes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"stdin", "stdout"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// firstLine reads one line from r, failing the test when none arrives.
func firstLine(t *testing.T, r *os.File) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		return line
	case <-time.After(15 * time.Second):
		t.Fatal("the command wrote nothing to the stdout pipe")
		return ""
	}
}

// TestRunExec_ConnectsTheCommandToThePipes: the command reads the bridge's stdin
// pipe as its stdin and writes its stdout to the bridge's stdout pipe.
func TestRunExec_ConnectsTheCommandToThePipes(t *testing.T) {
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat on this machine")
	}
	dir := makePipes(t)
	cmd := helper(t, "-pipe-dir", dir, "--", cat)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// The command opens stdin first, so that end is opened first here too.
	writer := pipetest.Open(t, filepath.Join(dir, "stdin"), os.O_WRONLY)
	reader := pipetest.Open(t, filepath.Join(dir, "stdout"), os.O_RDONLY)
	if _, err := writer.WriteString("hello bridge\n"); err != nil {
		t.Fatal(err)
	}
	if line := firstLine(t, reader); line != "hello bridge\n" {
		t.Fatalf("the command echoed %q", line)
	}
}

func exitCode(t *testing.T, cmd *exec.Cmd) (int, string) {
	t.Helper()
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatal(err)
	}
	return ee.ExitCode(), string(out)
}

func TestRunExec_WithoutACommandExitsWithAMessage(t *testing.T) {
	code, out := exitCode(t, helper(t, "-pipe-dir", t.TempDir()))
	if code != 1 || !strings.Contains(out, "no command specified") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestRunExec_WithMissingPipesExitsWithAMessage(t *testing.T) {
	code, out := exitCode(t, helper(t, "-pipe-dir", filepath.Join(t.TempDir(), "absent"), "--", "cat"))
	if code != 1 || !strings.Contains(out, "open stdin pipe") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}
