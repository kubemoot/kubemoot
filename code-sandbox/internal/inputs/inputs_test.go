package inputs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mine  = "ns/crew/thread-a/"
	other = "ns/crew/thread-b/"
	key   = "k8s-config/001-resources_get"
)

func fixture(t *testing.T) Resolver {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{mine + key, other + key, other + "k8s-config/002-secret"} {
		path := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Resolver{Roots: []string{filepath.Join(t.TempDir(), "missing"), root}}
}

func TestShortKeyResolvesInsideTheThread(t *testing.T) {
	r := fixture(t)
	got, err := r.Resolve(mine, []string{key})
	if err != nil || len(got) != 1 || got[0].Name != key {
		t.Fatalf("got %v, %v", got, err)
	}
	if b, _ := os.ReadFile(got[0].Source); string(b) != mine+key {
		t.Fatalf("read %q, want the file of this thread", b)
	}
}

func TestLongKeyOfTheSameThreadResolves(t *testing.T) {
	got, err := fixture(t).Resolve(mine, []string{mine + key, key})
	if err != nil || len(got) != 1 || got[0].Name != key {
		t.Fatalf("the long and the short key are one input: got %v, %v", got, err)
	}
}

func TestKeyOfAnotherThreadIsRefused(t *testing.T) {
	r := fixture(t)
	for _, k := range []string{other + key, "ns/crew/thread-b/k8s-config/002-secret", "../" + other + key,
		"k8s-config/../../thread-b/x", "/etc/passwd"} {
		if _, err := r.Resolve(mine, []string{k}); err == nil {
			t.Errorf("key %q: want a refusal", k)
		}
	}
	_, err := r.Resolve(mine, []string{other + key})
	if err == nil || !strings.Contains(err.Error(), "not a result of this discussion") {
		t.Fatalf("error %v should say the key is not of this discussion", err)
	}
}

func TestOnlyThisThreadsResultsExist(t *testing.T) {
	// k8s-config/002-secret exists in the other thread only.
	_, err := fixture(t).Resolve(mine, []string{"k8s-config/002-secret"})
	if err == nil || !strings.Contains(err.Error(), "no stored result") {
		t.Fatalf("error %v should say the result does not exist here", err)
	}
}

func TestUnknownPatternAndOversizedRequestsAreRefused(t *testing.T) {
	r := fixture(t)
	if _, err := r.Resolve(mine, []string{"k8s-config/009-nothing"}); err == nil {
		t.Error("an unknown key must be refused")
	}
	if _, err := r.Resolve(mine, []string{"k8s-config/*"}); err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Errorf("a pattern must be refused, got %v", err)
	}
	if _, err := r.Resolve(mine, make([]string, MaxInputs+1)); err == nil {
		t.Error("too many keys must be refused")
	}
}

func TestNoThreadRefusesKeysButNoKeysIsFine(t *testing.T) {
	r := fixture(t)
	if _, err := r.Resolve("", []string{key}); err == nil {
		t.Error("a call outside a discussion has no stored results")
	}
	if got, err := r.Resolve("", nil); err != nil || got != nil {
		t.Errorf("no inputs asked: got %v, %v", got, err)
	}
}

func TestPrefixFromMeta(t *testing.T) {
	cases := map[string]string{mine: mine, "": "", "ns/crew/": "", "ns/crew/t": "", "ns/../t/": "",
		"ns/crew/t/x/": "", "ns/crew/t y/": ""}
	for in, want := range cases {
		if got := PrefixFromMeta(map[string]any{MetaKey: in}); got != want {
			t.Errorf("prefix %q: got %q want %q", in, got, want)
		}
	}
	if PrefixFromMeta(nil) != "" || PrefixFromMeta(map[string]any{MetaKey: 7}) != "" {
		t.Error("a missing or non-string entry is no thread")
	}
}
