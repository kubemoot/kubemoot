package runner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/inputs"
)

const (
	threadA = "ns/crew/thread-a/"
	threadB = "ns/crew/thread-b/"
)

// workspace builds a workspace directory with one result for each of two discussions.
func workspace(t *testing.T) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "workspace")
	for path, body := range map[string]string{
		threadA + "k8s-config/001-resources_get": "mine\n",
		threadB + "k8s-config/001-resources_get": "other discussion\n",
	} {
		full := filepath.Join(ws, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

func confined(t *testing.T, ws string) *Runner {
	t.Helper()
	r := newRunner(t)
	r.WorkspaceDirs = []string{ws}
	r.Timeout = 20 * time.Second
	probe, err := r.Execute(context.Background(), "bash", "true", WithThread(threadA))
	if err != nil || probe.ExitCode != 0 {
		skipOrFail(t, "this host does not allow the user namespaces confinement needs: %v %+v", err, probe)
	}
	return r
}

// skipOrFail skips a confinement test on a host without user namespaces, and fails it where CI or
// REQUIRE_CONFINEMENT says the host must have them, so a missing capability cannot pass silently.
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" || os.Getenv("REQUIRE_CONFINEMENT") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func TestProgramSeesOnlyItsOwnDiscussionInTheWorkspace(t *testing.T) {
	ws := workspace(t)
	r := confined(t, ws)
	code := "import os\nprint(sorted(os.listdir(" + pyq(ws) + ")))\n" +
		"print(open(" + pyq(ws+"/k8s-config/001-resources_get") + ").read().strip())\n" +
		"for p in (" + pyq(ws+"/"+threadB+"k8s-config/001-resources_get") + ", " + pyq(ws+"/ns") + "):\n" +
		"    print(p.rsplit('/',1)[-1], os.path.exists(p))\n"
	res, err := r.Execute(context.Background(), "python", code, WithThread(threadA))
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: %v %+v", err, res)
	}
	want := "['k8s-config']\nmine\n001-resources_get False\nns False\n"
	if res.Stdout != want {
		t.Fatalf("program saw:\n%s\nwant:\n%s", res.Stdout, want)
	}
}

func TestProgramWithoutADiscussionSeesAnEmptyWorkspace(t *testing.T) {
	ws := workspace(t)
	res := execute(t, confined(t, ws), "bash", "ls -A "+ws+" | wc -l")
	if strings.TrimSpace(res.Stdout) != "0" {
		t.Fatalf("the workspace should look empty, got %q (%s)", res.Stdout, res.Stderr)
	}
}

func TestProgramCannotGetTheWorkspaceBackOrReachTheServer(t *testing.T) {
	ws := workspace(t)
	r := confined(t, ws)
	t.Setenv("SANDBOX_TEST_SECRET", "server-environment-secret")
	pid := itoa(os.Getpid())
	code := "umount " + ws + " 2>&1; mount --bind / /mnt 2>&1; ls " + ws + "/ns 2>&1; " +
		"cat /proc/" + pid + "/root" + ws + "/" + threadB + "k8s-config/001-resources_get 2>&1; " +
		"cat /proc/" + pid + "/environ 2>&1; ls /proc/" + pid + "/fd 2>&1; ls /proc/" + pid + "/cwd/ 2>&1; " +
		"grep -c '^CapEff:\\s*0*$' /proc/self/status"
	res := execute(t, r, "bash", code)
	for _, leak := range []string{"other discussion", "server-environment-secret"} {
		if strings.Contains(res.Stdout, leak) {
			t.Fatalf("the server's %q was readable:\n%s", leak, res.Stdout)
		}
	}
	if strings.Count(res.Stdout, "Permission denied")+strings.Count(res.Stdout, "No such file") < 4 {
		t.Fatalf("the server's /proc entries should be out of reach:\n%s", res.Stdout)
	}
	if !strings.HasSuffix(strings.TrimSpace(res.Stdout), "1") {
		t.Fatalf("the program still holds capabilities:\n%s", res.Stdout)
	}
}

func TestUnconfinedRunLeavesTheWorkspaceVisible(t *testing.T) {
	ws := workspace(t)
	r := newRunner(t)
	r.WorkspaceDirs = []string{ws}
	r.Unconfined = true
	res := execute(t, r, "bash", "cat "+ws+"/"+threadB+"k8s-config/001-resources_get")
	if !strings.Contains(res.Stdout, "other discussion") {
		t.Fatalf("an unconfined run should read the file, got %q", res.Stdout)
	}
}

func TestMissingWorkspaceNeedsNoConfinement(t *testing.T) {
	r := newRunner(t)
	r.WorkspaceDirs = []string{filepath.Join(t.TempDir(), "absent")}
	res := execute(t, r, "bash", "echo ok")
	if strings.TrimSpace(res.Stdout) != "ok" {
		t.Fatalf("got %q", res.Stdout)
	}
}

func TestStagedInputIsAFileTheProgramCountsOver(t *testing.T) {
	ws := workspace(t)
	csv := "name,state\n"
	want := 0
	for i := 0; i < 1500; i++ {
		state := "active"
		if i%7 == 0 {
			state = "decommissioned"
			want++
		}
		csv += "host-" + itoa(i) + "," + state + "\n"
	}
	yaml := "apiVersion: v1\nkind: ConfigMap\ndata:\n  hosts.csv: |\n" + indent(csv)
	full := filepath.Join(ws, threadA, "k8s-config/002-resources_get")
	if err := os.WriteFile(full, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	r := confined(t, ws)
	in := openInput(t, "k8s-config/002-resources_get", full)
	code := "n = sum(1 for line in open('inputs/k8s-config/002-resources_get') if line.rstrip().endswith(',decommissioned'))\nprint(n)\n"
	res, err := r.Execute(context.Background(), "python", code, WithThread(threadA),
		WithInputs([]inputs.Input{in}))
	if err != nil || strings.TrimSpace(res.Stdout) != itoa(want) {
		t.Fatalf("count = %q (%v), want %d; stderr %s", res.Stdout, err, want, res.Stderr)
	}
}

func TestInputNamesMustBePlain(t *testing.T) {
	in := openInput(t, "../escape", "/etc/hostname")
	_, err := newRunner(t).Execute(context.Background(), "bash", "true", WithInputs([]inputs.Input{in}))
	if err == nil {
		t.Fatal("a name that leaves inputs/ must be refused")
	}
}

func pyq(s string) string { return "'" + s + "'" }

func itoa(n int) string { return strconv.Itoa(n) }

func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if line != "" {
			b.WriteString("    " + line)
		}
	}
	return b.String()
}

// openInput opens path as a staged input of the given name; the test closes it.
func openInput(t *testing.T, name, path string) inputs.Input {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return inputs.Input{Name: name, File: f}
}
