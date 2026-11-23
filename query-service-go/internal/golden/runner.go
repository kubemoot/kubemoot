package golden

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const sourcePlaceholder = "{source}"

// Mismatch is one query whose two answers differ.
type Mismatch struct {
	ID    string
	Diffs []string
}

// Report is the outcome of a replay.
type Report struct {
	Total      int
	Mismatches []Mismatch
}

// Runner replays queries one at a time against two bases. A base is a URL with an
// optional {source} placeholder, for example
//
//	A: http://pilot-docs-query.pilot:8000
//	B: http://pilot-query.pilot:8000/sources/{source}
type Runner struct {
	A, B      string
	Tolerance float64
	Client    *http.Client
}

// Run sends each query to both services, serially, and collects the differences. A
// transport failure is reported as a mismatch of that query, not a stop.
func (r *Runner) Run(ctx context.Context, queries []Query) Report {
	tol := r.Tolerance
	if tol <= 0 {
		tol = DefaultTolerance
	}
	rep := Report{Total: len(queries)}
	for _, q := range queries {
		diffs := r.compare(ctx, q, tol)
		if len(diffs) > 0 {
			rep.Mismatches = append(rep.Mismatches, Mismatch{ID: q.ID, Diffs: diffs})
		}
	}
	return rep
}

func (r *Runner) compare(ctx context.Context, q Query, tol float64) []string {
	a, errA := r.send(ctx, r.A, q)
	b, errB := r.send(ctx, r.B, q)
	if errA != nil || errB != nil {
		return []string{fmt.Sprintf("request failed: A: %v; B: %v", errA, errB)}
	}
	return Diff(q, a, b, tol)
}

func (r *Runner) send(ctx context.Context, base string, q Query) (Response, error) {
	url := strings.ReplaceAll(base, sourcePlaceholder, q.Source) + q.Path
	req, err := http.NewRequestWithContext(ctx, q.Method, url, bytes.NewReader(q.Body))
	if err != nil {
		return Response{}, err
	}
	if len(q.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	return decodeResponse(resp.StatusCode, raw), nil
}

func decodeResponse(status int, raw []byte) Response {
	out := Response{Status: status, Raw: string(raw)}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body any
	if dec.Decode(&body) == nil {
		out.Body = body
	}
	return out
}
