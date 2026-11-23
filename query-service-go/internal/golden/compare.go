package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

// DefaultTolerance is how far two scores may differ and still match.
const DefaultTolerance = 1e-6

// Response is what one service answered.
type Response struct {
	Status int
	Body   any // decoded JSON (numbers as json.Number), nil when the body was not JSON
	Raw    string
}

// Diff lists how two answers to the same query differ; empty means they match.
func Diff(q Query, a, b Response, tol float64) []string {
	if a.Status != b.Status {
		return []string{fmt.Sprintf("status: %d vs %d", a.Status, b.Status)}
	}
	switch {
	case a.Status == 200 && q.Path == "/query":
		return diffQuery(a.Body, b.Body, tol)
	case a.Status == 200:
		return diffWhole(a.Body, b.Body)
	case a.Status == 422:
		return diffValidation(a.Body, b.Body)
	case a.Status == 404:
		return diffField(a.Body, b.Body, "detail")
	}
	return nil // other errors: the status is the contract, the message is the driver's
}

func diffWhole(a, b any) []string {
	if !reflect.DeepEqual(a, b) {
		return []string{fmt.Sprintf("body: %s vs %s", canon(a), canon(b))}
	}
	return nil
}

func diffField(a, b any, field string) []string {
	av, bv := get(a, field), get(b, field)
	if !reflect.DeepEqual(av, bv) {
		return []string{fmt.Sprintf("%s: %s vs %s", field, canon(av), canon(bv))}
	}
	return nil
}

// diffValidation compares the 422 detail lists by type and loc; the message wording and
// echoed input may differ between pydantic and the Go validator.
func diffValidation(a, b any) []string {
	pa, pb := problems(a), problems(b)
	if !reflect.DeepEqual(pa, pb) {
		return []string{fmt.Sprintf("validation: %s vs %s", canon(pa), canon(pb))}
	}
	return nil
}

// location is an error's loc; for a JSON syntax error only "body" counts, because the
// offset that follows depends on the JSON parser.
func location(item any) any {
	loc, _ := get(item, "loc").([]any)
	if get(item, "type") == "json_invalid" && len(loc) > 0 {
		return loc[:1]
	}
	return loc
}

func problems(body any) []string {
	list, _ := get(body, "detail").([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, canon(get(item, "type"))+" "+canon(location(item)))
	}
	return out
}

type hit struct {
	key   string // content and canonical metadata
	score float64
}

func diffQuery(a, b any, tol float64) []string {
	var out []string
	for _, f := range []string{"query", "collection"} {
		out = append(out, diffField(a, b, f)...)
	}
	ha, errA := hits(a)
	hb, errB := hits(b)
	if errA != nil || errB != nil {
		return append(out, fmt.Sprintf("results unreadable: %v / %v", errA, errB))
	}
	if len(ha) != len(hb) {
		return append(out, fmt.Sprintf("result count: %d vs %d", len(ha), len(hb)))
	}
	if bad := diffScores(ha, hb, tol); bad != nil {
		return append(out, bad...)
	}
	return append(out, diffOrder(ha, hb, tol)...)
}

func hits(body any) ([]hit, error) {
	list, ok := get(body, "results").([]any)
	if !ok {
		return nil, fmt.Errorf("no results array")
	}
	out := make([]hit, len(list))
	for i, item := range list {
		n, ok := get(item, "score").(json.Number)
		if !ok {
			return nil, fmt.Errorf("result %d has no numeric score", i)
		}
		s, err := n.Float64()
		if err != nil {
			return nil, err
		}
		out[i] = hit{key: canon(get(item, "content")) + "\x00" + canon(get(item, "metadata")), score: s}
	}
	return out, nil
}

func diffScores(a, b []hit, tol float64) []string {
	var out []string
	for i := range a {
		if math.Abs(a[i].score-b[i].score) > tol {
			out = append(out, fmt.Sprintf("score at rank %d: %.9f vs %.9f", i+1, a[i].score, b[i].score))
		}
	}
	return out
}

// diffOrder walks the ranks in tie groups (consecutive scores within tol of the group's
// first) and requires each group to hold the same rows in either order.
func diffOrder(a, b []hit, tol float64) []string {
	var out []string
	for start := 0; start < len(a); {
		end := start + 1
		for end < len(a) && math.Abs(a[end].score-a[start].score) <= tol {
			end++
		}
		if !sameSet(a[start:end], b[start:end]) {
			out = append(out, fmt.Sprintf("rows at ranks %d-%d differ", start+1, end))
		}
		start = end
	}
	return out
}

func sameSet(a, b []hit) bool {
	ka, kb := keys(a), keys(b)
	return reflect.DeepEqual(ka, kb)
}

func keys(h []hit) []string {
	out := make([]string, len(h))
	for i, x := range h {
		out[i] = x.key
	}
	sort.Strings(out)
	return out
}

func get(v any, field string) any {
	m, _ := v.(map[string]any)
	return m[field]
}

// canon renders a decoded value as JSON with sorted keys, so equal values compare equal.
func canon(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSpace(buf.String())
}
