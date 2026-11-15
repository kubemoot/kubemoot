package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

// describingFetcher serves objects with the headers the agent runtime sets on a stored tool call.
type describingFetcher struct {
	fakeFetcher
	headers map[string]map[string]string
}

func (d describingFetcher) Describe(_ context.Context, key string) (store.Meta, error) {
	h, ok := d.headers[key]
	if !ok {
		return store.Meta{}, errors.New("no such key")
	}
	return store.Meta{Key: key, Headers: h}, nil
}

func callHeaders(extra ...string) map[string]string {
	h := map[string]string{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "pods_list"}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func countOf(t *testing.T, s *Server, key string) string {
	t.Helper()
	_, out, err := s.count(context.Background(), nil, keyIn{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	return out.Output
}

func TestCountOfAStoredJSONListIsItsItemCountNotItsLineCount(t *testing.T) {
	s := New(describingFetcher{
		fakeFetcher: fakeFetcher{data: map[string]string{"k": `[{"a":1},{"a":2},{"a":3}]`}},
		headers:     map[string]map[string]string{"k": callHeaders("kubemoot.rows", "3", "Content-Type", "application/json")},
	})

	if got := countOf(t, s, "k"); got != "3\n" {
		t.Fatalf("got %q", got)
	}
}

func TestCountReadsTheObjectWhenTheHeadersCannotAnswer(t *testing.T) {
	cases := []struct {
		key, body string
		headers   map[string]string // nil: the store knows no headers for the key
		want      string
	}{
		{"k-cut", "a\nb\n", callHeaders("kubemoot.rows", "99", "kubemoot.truncated", "true"), "2\n"},
		{"k-text", "a\nb\nc\n", callHeaders(), "3\n"},
		{"k-notacall", "x\n", map[string]string{"kubemoot.rows": "7"}, "1\n"},
		{"k-nohdrs", "a\nb\nc\nd\ne\n", nil, "5\n"},
	}
	data := map[string]string{}
	headers := map[string]map[string]string{}
	for _, tc := range cases {
		data[tc.key] = tc.body
		if tc.headers != nil {
			headers[tc.key] = tc.headers
		}
	}
	s := New(describingFetcher{fakeFetcher: fakeFetcher{data: data}, headers: headers})

	for _, tc := range cases {
		if got := countOf(t, s, tc.key); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.key, got, tc.want)
		}
	}
}

func selectOf(t *testing.T, s *Server, key string) (string, error) {
	t.Helper()
	_, out, err := s.selectCSV(context.Background(), nil, selectIn{Key: key, Columns: []string{"name"}})
	return out.Output, err
}

func TestSelectBelievesTheRecordedType(t *testing.T) {
	body := "NAME   READY\nfoo    1/1\n"
	s := New(describingFetcher{
		fakeFetcher: fakeFetcher{data: map[string]string{"typed": body, "plain": body, "legacy": body}},
		headers: map[string]map[string]string{
			"typed":  callHeaders("Content-Type", "text/plain; profile=kubectl-table"),
			"plain":  callHeaders("Content-Type", "text/plain"),
			"legacy": callHeaders(),
		},
	})

	if got, err := selectOf(t, s, "typed"); err != nil || got != "name\nfoo\n" {
		t.Errorf("typed: %q, %v", got, err)
	}
	if got, err := selectOf(t, s, "legacy"); err != nil || got != "name\nfoo\n" {
		t.Errorf("an object without a type is sniffed: %q, %v", got, err)
	}
	if _, err := selectOf(t, s, "plain"); err == nil {
		t.Error("a plain text type is not a table, whatever the bytes look like")
	}
	if _, err := selectOf(t, s, ""); err == nil {
		t.Error("an empty key is refused")
	}
}

func TestSelectReadsACutKubectlTableByWhatItHolds(t *testing.T) {
	cut := "NAME   READY\nfoo    1/1\nba ...[truncated]"
	s := New(describingFetcher{
		fakeFetcher: fakeFetcher{data: map[string]string{"cutkey": cut}},
		headers: map[string]map[string]string{
			"cutkey": callHeaders("Content-Type", "text/plain; profile=kubectl-table", "kubemoot.truncated", "true"),
		},
	})

	got, err := selectOf(t, s, "cutkey")

	if err != nil || !strings.HasPrefix(got, "name\nfoo\n") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSelectOnACutPlainTextResultIsNotATable(t *testing.T) {
	s := New(describingFetcher{
		fakeFetcher: fakeFetcher{data: map[string]string{"cutplain": "just words ...[truncated]"}},
		headers: map[string]map[string]string{
			"cutplain": callHeaders("Content-Type", "text/plain", "kubemoot.truncated", "true"),
		},
	})

	if _, err := selectOf(t, s, "cutplain"); err == nil {
		t.Fatal("plain text is not a table")
	}
}
