package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flagQueries = "-queries"

func serve(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"query":"q","collection":"c","results":[{"content":"` + content + `","metadata":{},"score":0.5}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeSet(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "set.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"one","source":"s","body":{"query":"q"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExitCodes(t *testing.T) {
	same, other := serve(t, "x"), serve(t, "y")
	set := writeSet(t)
	cases := []struct {
		name string
		args []string
		want int
		out  string
	}{
		{"match", []string{flagQueries, set, "-a", same.URL, "-b", same.URL}, 0, "1 queries, 1 matched, 0 differed"},
		{"differ", []string{flagQueries, set, "-a", same.URL, "-b", other.URL}, 1, "MISMATCH one"},
		{"missing flags", []string{flagQueries, set}, 2, ""},
		{"no such file", []string{flagQueries, "/nonexistent", "-a", "x", "-b", "y"}, 2, ""},
		{"bad flag", []string{"-nope"}, 2, ""},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if got := run(c.args, &out, &errOut); got != c.want || !strings.Contains(out.String(), c.out) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", c.name, got, out.String(), errOut.String())
		}
	}
}
