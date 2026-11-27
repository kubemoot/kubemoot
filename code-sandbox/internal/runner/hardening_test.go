package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/inputs"
)

func TestViewIsReadOnlySoNoLinkOrForgeryCanBePlanted(t *testing.T) {
	ws := workspace(t)
	r := confined(t, ws)
	code := "cd " + ws + "/k8s-config; ln -s /etc/passwd evil 2>&1; { echo forged > 001-resources_get; } 2>&1; " +
		"touch new 2>&1; cat 001-resources_get"
	res, err := r.Execute(context.Background(), "bash", code, WithThread(threadA))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.Stdout, "Read-only file system") < 3 || !strings.Contains(res.Stdout, "mine") {
		t.Fatalf("the discussion's folder must be read-only:\n%s", res.Stdout)
	}
	if _, err := os.Lstat(filepath.Join(ws, threadA, "k8s-config", "evil")); err == nil {
		t.Fatal("a link was planted in the stored results")
	}
}

func TestStageRefusesALinkAndAnOversizedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	run := t.TempDir()
	if err := stage(context.Background(), run, []inputs.Input{{Name: "a/link", Source: link}}); err == nil {
		t.Fatal("a symbolic link must not be copied")
	}
	if _, err := os.Stat(filepath.Join(run, "inputs", "a", "link")); err == nil {
		t.Fatal("the link's target was copied")
	}
	if _, err := copyFile(context.Background(), target, filepath.Join(run, "big"), 3); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Fatalf("a file over the limit must be refused, got %v", err)
	}
	if _, err := copyFile(context.Background(), target, filepath.Join(run, "ok"), 6); err != nil {
		t.Fatalf("a file at the limit is fine: %v", err)
	}
}

func TestCopyStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyFile(ctx, src, filepath.Join(t.TempDir(), "d"), 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentRunsCannotReadEachOthersFiles(t *testing.T) {
	ws := workspace(t)
	r := confined(t, ws)
	src := filepath.Join(ws, threadA, "k8s-config", "001-resources_get")
	holder := "import time\nprint(open('inputs/k8s-config/001-resources_get').read().strip(), flush=True)\n" +
		"open('ready','w').write('x')\ntime.sleep(3)\n"
	// The second program looks for the first one's files under the shared temporary root.
	peek := "import glob, os\nfor _ in range(30):\n    if glob.glob('/tmp/code-sandbox-*/inputs'):\n        break\n" +
		"    import time; time.sleep(0.1)\n" +
		"print('visible:', glob.glob('/tmp/code-sandbox-*/inputs/*/*'), sorted(os.listdir(os.path.dirname(os.getcwd()))))\n"
	r.TempDir = "" // the OS temporary directory, shared by every run
	var wg sync.WaitGroup
	var holderRes, peekRes Result
	wg.Add(2)
	go func() {
		defer wg.Done()
		holderRes, _ = r.Execute(context.Background(), "python", holder, WithThread(threadA),
			WithInputs([]inputs.Input{{Name: "k8s-config/001-resources_get", Source: src}}))
	}()
	go func() {
		defer wg.Done()
		time.Sleep(500 * time.Millisecond)
		peekRes, _ = r.Execute(context.Background(), "python", peek, WithThread(threadB))
	}()
	wg.Wait()
	if !strings.Contains(holderRes.Stdout, "mine") {
		t.Fatalf("the first run should read its input: %+v", holderRes)
	}
	if !strings.Contains(peekRes.Stdout, "visible: [] [") || strings.Count(peekRes.Stdout, "code-sandbox-") != 1 {
		t.Fatalf("the second run must see only its own run directory under its temporary root:\n%s%s",
			peekRes.Stdout, peekRes.Stderr)
	}
}

func TestWorkspaceThatCannotBeExaminedStopsTheRun(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can examine any path")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	r := newRunner(t)
	r.WorkspaceDirs = []string{filepath.Join(locked, "workspace")}
	if _, err := r.Execute(context.Background(), "bash", "echo ran"); err == nil ||
		!strings.Contains(err.Error(), "cannot be examined") {
		t.Fatalf("an unreadable workspace path must stop the run, got %v", err)
	}
}

func TestHelperFailureIsAToolErrorNotAProgramExit(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "helper.sh")
	script := "#!/bin/sh\necho 'code-sandbox: the workspace could not be hidden from the program: boom' >&2\nexit 125\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRunner(t)
	r.Self = fake
	_, err := r.Execute(context.Background(), "bash", "echo ran")
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "SANDBOX_CONFINEMENT=off") {
		t.Fatalf("got %v", err)
	}
}

func TestProgramThatExits125ItselfIsStillAResult(t *testing.T) {
	res := execute(t, confined(t, workspace(t)), "bash", "exit 125")
	if res.ExitCode != 125 {
		t.Fatalf("got %+v", res)
	}
}
