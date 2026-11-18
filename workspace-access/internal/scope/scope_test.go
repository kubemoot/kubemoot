package scope

import (
	"errors"
	"os"
	"testing"
)

const teamA = "team-a"

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func noFile(string) ([]byte, error) { return nil, os.ErrNotExist }

func TestReferenceFilterIsNamespaced(t *testing.T) {
	if got := ReferenceFilter(teamA); got != "kubemoot.artifacts.team-a.>" {
		t.Errorf("filter = %q", got)
	}
	if ReferenceFilter(teamA) == ReferenceFilter("team-b") {
		t.Error("two namespaces must not share a filter")
	}
}

func TestMaterializerSubjectFromNamespaceEnv(t *testing.T) {
	got, err := MaterializerSubject(envOf(map[string]string{NamespaceEnv: teamA}), noFile)
	if err != nil || got != "kubemoot.artifacts.team-a.>" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestMaterializerSubjectFallsBackToServiceAccountFile(t *testing.T) {
	file := func(p string) ([]byte, error) {
		if p != ServiceAccountNamespaceFile {
			t.Errorf("read %q", p)
		}
		return []byte("team-b\n"), nil
	}
	got, err := MaterializerSubject(envOf(nil), file)
	if err != nil || got != "kubemoot.artifacts.team-b.>" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestMaterializerSubjectOverride(t *testing.T) {
	got, err := MaterializerSubject(envOf(map[string]string{SubjectEnv: "custom.>"}), noFile)
	if err != nil || got != "custom.>" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestMaterializerSubjectFailsWithoutNamespace(t *testing.T) {
	if _, err := MaterializerSubject(envOf(nil), noFile); !errors.Is(err, ErrNoNamespace) {
		t.Fatalf("want ErrNoNamespace, got %v", err)
	}
	blank := func(string) ([]byte, error) { return []byte(" \n"), nil }
	if _, err := MaterializerSubject(envOf(nil), blank); !errors.Is(err, ErrNoNamespace) {
		t.Fatalf("blank file: want ErrNoNamespace, got %v", err)
	}
	if _, err := MaterializerSubject(envOf(map[string]string{NamespaceEnv: "a.b"}), noFile); err == nil {
		t.Fatal("a dotted namespace must be rejected")
	}
}
