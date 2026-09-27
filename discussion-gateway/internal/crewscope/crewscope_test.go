package crewscope

import (
	"errors"
	"os"
	"testing"
)

func mustScope(t *testing.T, ns, crew string) Scope {
	t.Helper()
	s, err := New(ns, crew)
	if err != nil {
		t.Fatalf("New(%q, %q): %v", ns, crew, err)
	}
	return s
}

func TestSubjectsCarryNamespaceBeforeCrew(t *testing.T) {
	s := mustScope(t, "team-a", "pilot")
	cases := map[string]string{
		"request": s.RequestSubject(),
		"discuss": s.DiscussSubject("broadcast", "t-1"),
		"filter":  s.DiscussFilter(),
	}
	want := map[string]string{
		"request": "kubemoot.request.team-a.pilot",
		"discuss": "kubemoot.discuss.team-a.pilot.broadcast.t-1",
		"filter":  "kubemoot.discuss.team-a.pilot.>",
	}
	for k, got := range cases {
		if got != want[k] {
			t.Errorf("%s = %q, want %q", k, got, want[k])
		}
	}
}

func TestSameCrewNameInTwoNamespacesDiffers(t *testing.T) {
	a := mustScope(t, "team-a", "pilot")
	b := mustScope(t, "team-b", "pilot")
	if a.RequestSubject() == b.RequestSubject() {
		t.Errorf("request subjects collide: %q", a.RequestSubject())
	}
	if a.DiscussFilter() == b.DiscussFilter() {
		t.Errorf("discuss filters collide: %q", a.DiscussFilter())
	}
	if a.DiscussSubject("general", "t") == b.DiscussSubject("general", "t") {
		t.Errorf("discuss subjects collide")
	}
}

func TestNewRejectsInvalidTokens(t *testing.T) {
	for _, c := range []struct{ ns, crew string }{
		{"", "pilot"},
		{"team-a", ""},
		{"team.a", "pilot"},
		{"team-a", "pi.lot"},
		{"team-a", "pilot>"},
		{"team-a", "*"},
		{"team a", "pilot"},
	} {
		if _, err := New(c.ns, c.crew); err == nil {
			t.Errorf("New(%q, %q) accepted an invalid token", c.ns, c.crew)
		}
	}
}

func noFile(string) ([]byte, error) { return nil, os.ErrNotExist }

func TestResolveNamespacePrefersEnv(t *testing.T) {
	env := func(k string) string {
		if k == NamespaceEnv {
			return " team-a "
		}
		return ""
	}
	file := func(string) ([]byte, error) { return []byte("from-file"), nil }
	ns, err := ResolveNamespace(env, file)
	if err != nil || ns != "team-a" {
		t.Fatalf("got %q, %v; want team-a", ns, err)
	}
}

func TestResolveNamespaceFallsBackToServiceAccountFile(t *testing.T) {
	var readPath string
	file := func(p string) ([]byte, error) {
		readPath = p
		return []byte("team-b\n"), nil
	}
	ns, err := ResolveNamespace(func(string) string { return "" }, file)
	if err != nil || ns != "team-b" {
		t.Fatalf("got %q, %v; want team-b", ns, err)
	}
	if readPath != ServiceAccountNamespaceFile {
		t.Errorf("read %q, want %q", readPath, ServiceAccountNamespaceFile)
	}
}

func TestResolveNamespaceFailsWhenBothMissing(t *testing.T) {
	if _, err := ResolveNamespace(func(string) string { return "" }, noFile); !errors.Is(err, ErrNoNamespace) {
		t.Fatalf("want ErrNoNamespace, got %v", err)
	}
	empty := func(string) ([]byte, error) { return []byte("  \n"), nil }
	if _, err := ResolveNamespace(func(string) string { return "" }, empty); !errors.Is(err, ErrNoNamespace) {
		t.Fatalf("empty file: want ErrNoNamespace, got %v", err)
	}
}

func TestResolveNamespaceRejectsDottedValue(t *testing.T) {
	env := func(string) string { return "a.b" }
	if _, err := ResolveNamespace(env, noFile); err == nil {
		t.Fatal("a dotted namespace must be rejected")
	}
}
