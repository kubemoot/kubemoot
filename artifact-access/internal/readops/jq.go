package readops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/itchyny/gojq"
	"gopkg.in/yaml.v3"
)

// Jq runs the gojq program over a JSON or YAML artifact and returns one JSON result
// per line. YAML is a superset of JSON, so a Kubernetes object stored as YAML works
// as well as a JSON one. An artifact holding several values (a stream of JSON values
// or a multi-document YAML file) runs the program once per value, as jq does. The
// input is capped at maxInputBytes because jq must parse it whole; the result stream
// is separately bounded by maxBytes. Beyond either cap the caller should use the
// compute sandbox. A table or plain text is rejected with the tool that fits it.
func Jq(r io.Reader, program string, maxInputBytes int64, maxBytes int) (string, error) {
	q, err := gojq.Parse(program)
	if err != nil {
		return "", fmt.Errorf("parse jq: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxInputBytes {
		return "", fmt.Errorf("input exceeds jq cap of %d bytes; use a compute sandbox", maxInputBytes)
	}
	docs, err := decodeDocuments(data)
	if err != nil {
		return "", err
	}
	out := newLimitedBuilder(maxBytes)
	for _, doc := range docs {
		if full, err := runProgram(q, doc, out); err != nil || full {
			return out.String(), err
		}
	}
	return out.String(), nil
}

// runProgram feeds one value to the program and appends each result to out. It
// reports true once out is full.
func runProgram(q *gojq.Query, input any, out *limitedBuilder) (bool, error) {
	iter := q.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			return false, nil
		}
		if err, isErr := v.(error); isErr {
			return false, fmt.Errorf("jq: %w", err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return false, err
		}
		if !out.line(string(b)) {
			return true, nil
		}
	}
}

// decodeDocuments parses the artifact as one or more JSON values, falling back to
// YAML documents.
func decodeDocuments(data []byte) ([]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errEmpty
	}
	if docs, err := decodeJSON(data); err == nil {
		return docs, nil
	}
	docs, err := decodeYAML(data)
	if err != nil || !allStructured(docs) {
		return nil, notStructured(data)
	}
	return docs, nil
}

func decodeJSON(data []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var docs []any
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, v)
	}
}

func decodeYAML(data []byte) ([]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []any
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if v != nil { // an empty document between --- markers
			docs = append(docs, normalize(v))
		}
	}
}

// allStructured reports whether there is at least one document and every one is a
// mapping or a list. A bare scalar is what YAML makes of plain text.
func allStructured(docs []any) bool {
	for _, d := range docs {
		switch d.(type) {
		case map[string]any, []any:
		default:
			return false
		}
	}
	return len(docs) > 0
}

// notStructured explains that the artifact is neither JSON nor YAML and names the
// tools that fit its shape.
func notStructured(data []byte) error {
	line, _ := firstDataLine(data[:min(len(data), sniffBytes)])
	shape, tools := "plain text", "artifact_grep, artifact_rows, or artifact_head"
	if isTableLine(line) {
		shape, tools = "a table", "artifact_select (columns by header name), artifact_grep, or artifact_rows"
	}
	return fmt.Errorf("the artifact is not JSON or YAML, it is %s starting %q; use %s",
		shape, truncate(strings.TrimSpace(line), 60), tools)
}

// normalize converts YAML-decoded values into the types gojq accepts.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = normalize(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
		return x
	default:
		return normalizeScalar(v)
	}
}

func normalizeScalar(v any) any {
	switch x := v.(type) {
	case int64:
		return int(x)
	case uint64:
		if x > math.MaxInt64 {
			return float64(x)
		}
		return int(x)
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return v
	}
}
