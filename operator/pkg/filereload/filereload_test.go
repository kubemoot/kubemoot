package filereload

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, file, text string, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// reading builds the concatenation of the files and counts the builds.
func reading(files ...string) (*Loader[string], *int) {
	builds := 0
	return New(func() (string, error) {
		builds++
		out := ""
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			out += string(b)
		}
		return out, nil
	}, files...), &builds
}

func TestAValueIsBuiltOnceUntilAFileChanges(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	cert, key := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
	write(t, cert, "c1", now)
	write(t, key, "k1", now)
	l, builds := reading(cert, key)
	for range 3 {
		if v, err := l.Get(); err != nil || v != "c1k1" {
			t.Fatalf("Get: %q %v", v, err)
		}
	}
	if *builds != 1 {
		t.Fatalf("built %d times for an unchanged pair", *builds)
	}
	write(t, key, "k2", now.Add(time.Minute)) // a change in the second file alone is seen
	if v, _ := l.Get(); v != "c1k2" || *builds != 2 {
		t.Fatalf("after the key changed: %q, %d builds", v, *builds)
	}
}

func TestAFileThatGoesAwayFailsClosedAndABuildErrorIsNotCached(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	write(t, file, "v1", time.Now())
	l, _ := reading(file)
	if _, err := l.Get(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if v, err := l.Get(); err == nil {
		t.Fatalf("a value %q was served with no file", v)
	}
	failing := New(func() (int, error) { return 0, errors.New("bad") }, file+"2")
	write(t, file+"2", "x", time.Now())
	if _, err := failing.Get(); err == nil {
		t.Fatal("a failed build was accepted")
	}
}

func TestOnReplaceSeesTheOldValueOnlyOnARebuild(t *testing.T) {
	file, now := filepath.Join(t.TempDir(), "f"), time.Now()
	write(t, file, "v1", now)
	l, _ := reading(file)
	var replaced []string
	l.OnReplace(func(old string) { replaced = append(replaced, old) })
	_, _ = l.Get()
	if len(replaced) != 0 {
		t.Fatalf("the first build replaced %v", replaced)
	}
	write(t, file, "v2", now.Add(time.Minute))
	_, _ = l.Get()
	if len(replaced) != 1 || replaced[0] != "v1" {
		t.Fatalf("replaced %v", replaced)
	}
}
