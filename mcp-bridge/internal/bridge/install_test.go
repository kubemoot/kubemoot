package bridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInstallFileCreatesAnExecutableCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeExecutable(t, src, "#!/bin/sh\necho hi\n")
	dst := filepath.Join(dir, "pipes", "bridge")
	if err := os.Mkdir(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installFile(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestInstallFileReplacesAnExistingFileAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	writeExecutable(t, dst, "old")
	writeExecutable(t, src, "new")
	if err := installFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("want only src and dst, got %d entries", len(entries))
	}
}

// A sidecar restart finds the main container executing the binary the previous
// start installed. Writing into it in place fails with "text file busy"; the
// install must still succeed and must not disturb the running process.
func TestInstallFileSucceedsWhileTheOldCopyIsRunning(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "bridge")
	body, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o755); err != nil {
		t.Fatal(err)
	}
	running := exec.Command(dst, "30")
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = running.Process.Kill(); _, _ = running.Process.Wait() })

	if f, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0); err == nil {
		_ = f.Close()
		t.Skip("this kernel allowed an in-place write to a running binary; nothing to prove")
	}
	if err := installFile(sleep, dst); err != nil {
		t.Fatalf("install over a running binary: %v", err)
	}
	if err := syscall.Kill(running.Process.Pid, 0); err != nil {
		t.Fatalf("the running process was disturbed: %v", err)
	}
}

func TestInstallFileReportsAMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := installFile(filepath.Join(dir, "missing"), filepath.Join(dir, "dst")); err == nil {
		t.Fatal("want an error for a missing source")
	}
}
