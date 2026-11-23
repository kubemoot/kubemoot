// Package golden replays a recorded query set against two query services and reports
// where their answers differ. It is how the Go service is shown to match the Python one
// before cutover: same status, same ordered content and metadata, scores within a
// tolerance, rows that tie on score compared as sets.
package golden

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Query is one line of a query set (JSONL, one object per line; blank lines and lines
// starting with # are skipped).
//
//	{"id":"fit-01","source":"pilot-docs","body":{"query":"how do I scale","top_k":5,"threshold":0.3}}
//	{"id":"info-1","source":"pilot-docs","method":"GET","path":"/info"}
//
// method defaults to POST when a body is present and GET otherwise; path defaults to
// /query. Body is sent as written, so a set can include invalid requests to compare
// the 422 answers.
type Query struct {
	ID     string          `json:"id"`
	Source string          `json:"source"`
	Method string          `json:"method,omitempty"`
	Path   string          `json:"path,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// ReadQuerySet parses a JSONL query set.
func ReadQuerySet(r io.Reader) ([]Query, error) {
	var out []Query
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		q, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, q)
	}
	return out, sc.Err()
}

func parseLine(line []byte) (Query, error) {
	var q Query
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		return q, err
	}
	if q.ID == "" || q.Source == "" {
		return q, fmt.Errorf("id and source are required")
	}
	return q.withDefaults()
}

func (q Query) withDefaults() (Query, error) {
	if string(bytes.TrimSpace(q.Body)) == "null" {
		q.Body = nil
	}
	if q.Path == "" {
		q.Path = "/query"
	}
	if !strings.HasPrefix(q.Path, "/") {
		return q, fmt.Errorf("path %q must start with /", q.Path)
	}
	if q.Method == "" {
		q.Method = "GET"
		if len(q.Body) > 0 {
			q.Method = "POST"
		}
	}
	return q, nil
}
