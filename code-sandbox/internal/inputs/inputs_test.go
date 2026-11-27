package inputs

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	crewDir = "crew"
	mine    = "ns/crew/thread-a/"
	other   = "ns/crew/thread-b/"
	key     = "k8s-config/001-resources_get"
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
	defer Close(got)
	if b, _ := io.ReadAll(got[0].File); string(b) != mine+key {
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

func TestPlantedLinksAreRefused(t *testing.T) {
	r := fixture(t)
	root := r.Roots[1]
	otherFile := filepath.Join(root, other, key)
	if err := os.Symlink(otherFile, filepath.Join(root, mine, "k8s-config", "003-planted")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, other, "k8s-config"), filepath.Join(root, mine, "linked-agent")); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"k8s-config/003-planted", "linked-agent/001-resources_get"} {
		_, err := r.Resolve(mine, []string{k})
		if !errors.Is(err, ErrLink) {
			t.Errorf("key %q: want a refusal for the link, got %v", k, err)
		}
	}
	if _, err := r.Resolve(mine, []string{key}); err != nil {
		t.Errorf("a plain file still resolves: %v", err)
	}
}

func TestADirectoryIsNotAResult(t *testing.T) {
	r := fixture(t)
	if err := os.MkdirAll(filepath.Join(r.Roots[1], mine, "k8s-config", "004-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(mine, []string{"k8s-config/004-dir"}); err == nil {
		t.Fatal("a directory is not a stored result")
	}
}

type vectors struct {
	Prefixes []struct {
		Value string `json:"value"`
		Valid bool   `json:"valid"`
	} `json:"prefixes"`
	KeysPrefix string `json:"keysPrefix"`
	Keys       []struct {
		Key    string `json:"key"`
		Inputs string `json:"inputs"`
	} `json:"keys"`
}

// TestSharedVectors runs the prefixes and keys the gateway and workspace-access run too.
func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/workspace-prefix-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, p := range v.Prefixes {
		if got := PrefixFromMeta(map[string]any{MetaKey: p.Value}) != ""; got != p.Valid {
			t.Errorf("prefix %q: valid = %v, want %v", p.Value, got, p.Valid)
		}
	}
	root := t.TempDir()
	file := filepath.Join(root, v.KeysPrefix, "agent", "001-x")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Resolver{Roots: []string{root}}
	for _, k := range v.Keys {
		_, err := r.Resolve(v.KeysPrefix, []string{k.Key})
		if (err == nil) != (k.Inputs == "ok") {
			t.Errorf("key %q: inputs = %v, want %s", k.Key, err, k.Inputs)
		}
	}
}

func TestOpenDirRefusesALinkAnywhereInThePath(t *testing.T) {
	r := fixture(t)
	root := r.Roots[1]
	if err := os.Symlink(filepath.Join(root, other), filepath.Join(root, "ns", crewDir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "ns", crewDir), filepath.Join(root, "ns", "alias")); err != nil {
		t.Fatal(err)
	}
	for _, parts := range [][]string{{"ns", crewDir, "linked"}, {"ns", "alias", "thread-a"}} {
		if f, err := OpenDir(root, parts); !errors.Is(err, ErrLink) {
			if f != nil {
				_ = f.Close()
			}
			t.Errorf("%v: want ErrLink, got %v", parts, err)
		}
	}
	f, err := OpenDir(root, []string{"ns", crewDir, "thread-a"})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := OpenDir(root, []string{"ns", crewDir, "missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing folder is not a link: %v", err)
	}
}
