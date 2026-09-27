package scope

import (
	"errors"
	"os"
	"testing"
)

const teamA = "team-a"

func noEnv(string) string { return "" }

func noFile(string) ([]byte, error) { return nil, os.ErrNotExist }

func TestResolveNamespacePrefersEnv(t *testing.T) {
	env := func(k string) string {
		if k == NamespaceEnv {
			return teamA
		}
		return ""
	}
	file := func(string) ([]byte, error) { return []byte("from-file"), nil }
	if ns, err := ResolveNamespace(env, file); err != nil || ns != teamA {
		t.Fatalf("got %q, %v", ns, err)
	}
}

func TestResolveNamespaceFallsBackToServiceAccountFile(t *testing.T) {
	var read string
	file := func(p string) ([]byte, error) { read = p; return []byte(teamA + "\n"), nil }
	if ns, err := ResolveNamespace(noEnv, file); err != nil || ns != teamA {
		t.Fatalf("got %q, %v", ns, err)
	}
	if read != ServiceAccountNamespaceFile {
		t.Errorf("read %q", read)
	}
}

func TestResolveNamespaceFailsWhenBothMissing(t *testing.T) {
	if _, err := ResolveNamespace(noEnv, noFile); !errors.Is(err, ErrNoNamespace) {
		t.Fatalf("want ErrNoNamespace, got %v", err)
	}
	blank := func(string) ([]byte, error) { return []byte(" "), nil }
	if _, err := ResolveNamespace(noEnv, blank); !errors.Is(err, ErrNoNamespace) {
		t.Fatalf("blank file: want ErrNoNamespace, got %v", err)
	}
}

func TestResolveNamespaceRejectsDottedValue(t *testing.T) {
	if _, err := ResolveNamespace(func(string) string { return "a.b" }, noFile); err == nil {
		t.Fatal("a dotted namespace must be rejected")
	}
}
