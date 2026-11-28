package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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

func TestCopyRefusesAnOversizedFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(src, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := t.TempDir()
	in := openInput(t, "a", src)
	if _, err := copyFile(context.Background(), in.File, filepath.Join(run, "big"), 3); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Fatalf("a file over the limit must be refused, got %v", err)
	}
	if _, err := copyFile(context.Background(), in.File, filepath.Join(run, "ok"), 6); err != nil {
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
	in := openInput(t, "a", src)
	if _, err := copyFile(ctx, in.File, filepath.Join(t.TempDir(), "d"), 10); !errors.Is(err, context.Canceled) {
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
		in := openInput(t, "k8s-config/001-resources_get", src)
		holderRes, _ = r.Execute(context.Background(), "python", holder, WithThread(threadA),
			WithInputs([]inputs.Input{in}))
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
	r.Unconfined = false // the run stops on the workspace check, before any namespace is created
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
	needsConfinement(t)
	r := newRunner(t)
	r.Unconfined = false
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

func TestThreadFolderThatIsALinkIsNotShown(t *testing.T) {
	ws := workspace(t)
	// A link at the thread level that points at another discussion's folder.
	if err := os.Symlink(filepath.Join(ws, threadB), filepath.Join(ws, "ns/crew/thread-c")); err != nil {
		t.Fatal(err)
	}
	r := confined(t, ws)
	res, err := r.Execute(context.Background(), "bash", "ls -A "+ws+" | wc -l; cat "+ws+"/k8s-config/001-resources_get 2>&1",
		WithThread("ns/crew/thread-c/"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "other discussion") || !strings.HasPrefix(strings.TrimSpace(res.Stdout), "0") {
		t.Fatalf("a linked thread folder must show an empty workspace:\n%s", res.Stdout)
	}
	// A link in the middle of the thread path is refused the same way.
	if err := os.Symlink(filepath.Join(ws, "ns/crew"), filepath.Join(ws, "ns/linked")); err != nil {
		t.Fatal(err)
	}
	res, _ = r.Execute(context.Background(), "bash", "ls -A "+ws+" | wc -l", WithThread("ns/linked/thread-b/"))
	if strings.TrimSpace(res.Stdout) != "0" {
		t.Fatalf("a link inside the thread path must show an empty workspace:\n%s", res.Stdout)
	}
}

// hostPidOfRunUnder finds the process of a run whose program lives under dir, from outside the run.
func hostPidOfRunUnder(t *testing.T, dir string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			cmd, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
			if err == nil && strings.Contains(string(cmd), dir) && strings.Contains(string(cmd), "main.py") &&
				!strings.Contains(string(cmd), "--code-sandbox-confine") {
				return e.Name()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the other run's process was not found")
	return ""
}

func TestAnotherLiveRunsProcEntriesAreDenied(t *testing.T) {
	r := confined(t, workspace(t))
	r.TempDir = t.TempDir()
	started := make(chan Result, 1)
	go func() {
		res, _ := r.Execute(context.Background(), "python", "import time\ntime.sleep(4)\n")
		started <- res
	}()
	pid := hostPidOfRunUnder(t, r.TempDir)
	code := "for e in cwd root environ fd; do cat /proc/" + pid + "/$e 2>&1 | head -c 200; ls /proc/" + pid + "/$e/ 2>&1 | head -3; done; " +
		"echo LISTED:; ls /proc | grep -c '^[0-9]'"
	res := execute(t, r, "bash", code)
	if strings.Contains(res.Stdout, "main.py") || strings.Contains(res.Stdout, "code-sandbox-") {
		t.Fatalf("the other run was readable through /proc/%s:\n%s", pid, res.Stdout)
	}
	if got := strings.Count(res.Stdout, "Permission denied") + strings.Count(res.Stdout, "No such file"); got < 4 {
		t.Fatalf("every /proc/%s entry should be denied or absent:\n%s", pid, res.Stdout)
	}
	<-started
}

func TestProgramSeesOnlyItsOwnProcesses(t *testing.T) {
	r := confined(t, workspace(t))
	var logged []string
	r.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	res := execute(t, r, "bash", "ls /proc | grep -c '^[0-9]'; echo $$")
	if len(logged) > 0 {
		skipOrFail(t, "this host keeps the old /proc; the ptrace rules apply instead: %v", logged)
	}
	lines := strings.Fields(res.Stdout)
	if len(lines) != 2 {
		t.Fatalf("got %q (%s)", res.Stdout, res.Stderr)
	}
	count, _ := strconv.Atoi(lines[0])
	pid, _ := strconv.Atoi(lines[1])
	if count == 0 || count > 5 || pid > 5 {
		t.Fatalf("the program should list a few processes of its own pid namespace: %d listed, shell pid %d", count, pid)
	}
}

func TestProcReportReachesTheServerOnce(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "helper.sh")
	script := "#!/bin/sh\necho 'proc-not-replaced: boom' >&3\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	needsConfinement(t)
	r := newRunner(t)
	r.Unconfined = false
	r.Self = fake
	var logged []string
	r.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	for i := 0; i < 3; i++ {
		if _, err := r.Execute(context.Background(), "bash", "true"); err != nil {
			t.Fatal(err)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "boom") || !strings.Contains(logged[0], "SANDBOX_REQUIRE_PROC") {
		t.Fatalf("the report should be logged once with its cause: %v", logged)
	}
}

func TestFolderSwappedForALinkBeforeTheHelperStartsShowsNoLinkTarget(t *testing.T) {
	ws := workspace(t)
	r := confined(t, ws)
	r.beforeStart = func() {
		a := filepath.Join(ws, threadA)
		if err := os.Rename(a, a[:len(a)-1]+".real"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(filepath.Join(ws, threadB), a[:len(a)-1]); err != nil {
			t.Error(err)
		}
	}
	res, err := r.Execute(context.Background(), "bash", "ls -A "+ws+" | wc -l; cat "+ws+"/k8s-config/* 2>&1",
		WithThread(threadA))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "other discussion") {
		t.Fatalf("the link's target was shown to the program:\n%s", res.Stdout)
	}
}

func TestProgramCannotUndoTheViewByUmountRemountOrANewNamespace(t *testing.T) {
	ws := workspace(t)
	r := confined(t, ws)
	file := ws + "/k8s-config/001-resources_get"
	code := "umount " + ws + " 2>&1; mount -o remount,rw,bind " + ws + " 2>&1; { echo x >> " + file + "; } 2>&1; " +
		"unshare -Urm sh -c 'mount -o remount,rw,bind " + ws + " 2>&1; mount -t tmpfs none " + ws + " 2>&1; " +
		"{ echo y >> " + file + "; } 2>&1' 2>&1; cat " + file
	res, err := r.Execute(context.Background(), "bash", code, WithThread(threadA))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "x\n") || strings.Contains(res.Stdout, "y\n") || !strings.Contains(res.Stdout, "mine") {
		t.Fatalf("the stored result was changed:\n%s", res.Stdout)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, threadA, "k8s-config/001-resources_get")); string(b) != "mine\n" {
		t.Fatalf("the file on disk changed: %q", b)
	}
}

func TestTemporaryDirectorySizeIsASetting(t *testing.T) {
	r := confined(t, workspace(t))
	r.TempSize = "1m"
	res := execute(t, r, "bash", "dd if=/dev/zero of=\"$(dirname \"$PWD\")/big\" bs=1M count=3 2>&1 | grep -c 'No space left'")
	if strings.TrimSpace(res.Stdout) != "1" {
		t.Fatalf("a 1m temporary directory should fill up:\n%s", res.Stdout)
	}
	r.TempSize = ""
	res = execute(t, r, "bash", "dd if=/dev/zero of=\"$(dirname \"$PWD\")/big\" bs=1M count=3 2>&1 | grep -c 'No space left'")
	if strings.TrimSpace(res.Stdout) != "0" {
		t.Fatalf("the default size holds 3 MiB:\n%s", res.Stdout)
	}
}

func TestProgramInheritsOnlyItsStandardDescriptors(t *testing.T) {
	r := confined(t, workspace(t))
	// A descriptor the server's own parent left open without close-on-exec, as a CI runner does.
	leak, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leak.Close() }()
	if _, err := unix.FcntlInt(leak.Fd(), unix.F_SETFD, 0); err != nil {
		t.Fatal(err)
	}
	code := "import os\nopen_fds = []\nfor fd in range(3, 256):\n" +
		"    try:\n        os.fstat(fd)\n        open_fds.append(fd)\n    except OSError:\n        pass\n" +
		"print(open_fds)\n"
	res, err := r.Execute(context.Background(), "python", code, WithThread(threadA))
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: %v %+v", err, res)
	}
	if res.Stdout != "[]\n" {
		t.Fatalf("program holds descriptors beyond 0, 1 and 2: %s", res.Stdout)
	}
}
