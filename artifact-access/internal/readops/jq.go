package readops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathbig "math/big"
	"strings"

	"github.com/itchyny/gojq"
	"go.yaml.in/yaml/v3"
)

// Jq runs the gojq program over a JSON or YAML artifact and returns one JSON result
// per line. YAML is a superset of JSON, so a Kubernetes object stored as YAML works
// as well as a JSON one. An artifact holding several values (a stream of JSON values
// or a multi-document YAML file) runs the program once per value, as jq does. Leading
// blank lines and "[tool]" section markers are ignored. The input is capped at
// maxInputBytes (<= 0 disables the cap) because jq must parse it whole; the result
// stream is separately bounded by maxBytes. Beyond either cap the caller should use
// the compute sandbox. A table or plain text is rejected with the tool that fits it.
func Jq(r io.Reader, program string, maxInputBytes int64, maxBytes int) (string, error) {
	q, err := gojq.Parse(program)
	if err != nil {
		return "", fmt.Errorf("parse jq: %w", err)
	}
	data, err := readCapped(r, maxInputBytes)
	if err != nil {
		return "", err
	}
	docs, err := decodeDocuments(data)
	if err != nil {
		return "", err
	}
	out := newLimitedBuilder(maxBytes)
	for i, doc := range docs {
		full, err := runProgram(q, doc, out)
		if err != nil && len(docs) > 1 {
			err = fmt.Errorf("document %d of %d: %w", i+1, len(docs), err)
		}
		if err != nil || full {
			return out.String(), err
		}
	}
	return out.String(), nil
}

func readCapped(r io.Reader, maxInputBytes int64) ([]byte, error) {
	if maxInputBytes <= 0 {
		return io.ReadAll(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxInputBytes {
		return nil, fmt.Errorf("input exceeds jq cap of %d bytes; use a compute sandbox", maxInputBytes)
	}
	return data, nil
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

// stripLeadingNoise drops the blank lines and tool-section markers a tooler writes
// before its payload.
func stripLeadingNoise(data []byte) []byte {
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		line := data
		if nl >= 0 {
			line = data[:nl]
		}
		if !skippable(string(line)) {
			return data
		}
		if nl < 0 {
			return nil
		}
		data = data[nl+1:]
	}
	return data
}

// decodeDocuments parses the artifact as one or more JSON values, falling back to
// YAML documents. An artifact that opens like JSON but fails to parse reports the
// JSON error, since YAML would only hide a truncation.
func decodeDocuments(data []byte) ([]any, error) {
	data = stripLeadingNoise(data)
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errEmpty
	}
	docs, jsonErr := decodeJSON(data)
	if jsonErr == nil {
		return docs, nil
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return nil, fmt.Errorf("the artifact looks like JSON but does not parse (truncated or malformed?): %w", jsonErr)
	}
	return decodeYAMLDocuments(data)
}

func decodeJSON(data []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
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
		docs = append(docs, jsonValue(v))
	}
}

// jsonValue replaces json.Number with the integer or float gojq expects, keeping
// integers beyond 2^53 exact.
func jsonValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		return numberValue(x.String())
	case map[string]any:
		for k, e := range x {
			x[k] = jsonValue(e)
		}
	case []any:
		for i, e := range x {
			x[i] = jsonValue(e)
		}
	}
	return v
}

// numberValue turns number text into an int, a big integer, or a float64.
func numberValue(text string) any {
	if bi, ok := new(mathbig.Int).SetString(strings.ReplaceAll(text, "_", ""), 0); ok {
		if bi.IsInt64() {
			return int(bi.Int64())
		}
		return bi
	}
	f, _ := new(mathbig.Float).SetString(text)
	if f == nil {
		return text
	}
	v, _ := f.Float64()
	return v
}

// maxYAMLValues bounds how many values a YAML artifact may expand to once aliases are
// followed, so an alias bomb in a small input cannot exhaust memory.
const maxYAMLValues = 1 << 20

func decodeYAMLDocuments(data []byte) ([]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []any
	budget := maxYAMLValues
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, yamlError(data, err)
		}
		v, err := nodeValue(&node, &budget)
		if err != nil {
			return nil, err
		}
		if v != nil { // an empty document between --- markers
			docs = append(docs, v)
		}
	}
	return checkStructured(data, docs)
}

// yamlError reports a parse failure, naming the table tools when the data is a table.
func yamlError(data []byte, err error) error {
	if tableShapeOf(data) != nil {
		return notStructured("a table", data)
	}
	return fmt.Errorf("the artifact is not valid JSON or YAML (%s); "+
		"use artifact_grep, artifact_rows, or artifact_head to read it as text", truncate(err.Error(), 120))
}

// checkStructured accepts documents that are all mappings or lists. A bare scalar is
// what YAML makes of plain text.
func checkStructured(data []byte, docs []any) ([]any, error) {
	if len(docs) == 0 {
		return nil, errors.New("the artifact has no content: only comments or document markers")
	}
	for i, d := range docs {
		switch d.(type) {
		case map[string]any, []any:
		default:
			if len(docs) > 1 {
				return nil, fmt.Errorf("YAML document %d of %d is a single value, not a mapping or list", i+1, len(docs))
			}
			return nil, notStructured(shapeName(data), data)
		}
	}
	return docs, nil
}

func tableShapeOf(data []byte) *tableShape {
	line, ok := firstDataLine(data[:min(len(data), sniffBytes)])
	if !ok {
		return nil
	}
	shape, isTable := parseTableHeader(line)
	if !isTable {
		return nil
	}
	return shape
}

func shapeName(data []byte) string {
	if tableShapeOf(data) != nil {
		return "a table"
	}
	return "plain text"
}

// notStructured explains that the artifact is neither JSON nor YAML data and names
// the tools that fit its shape.
func notStructured(shape string, data []byte) error {
	line, _ := firstDataLine(data[:min(len(data), sniffBytes)])
	tools := "artifact_grep, artifact_rows, or artifact_head"
	if shape == "a table" {
		tools = "artifact_select (columns by header name), artifact_grep, or artifact_rows"
	}
	return fmt.Errorf("the artifact is not JSON or YAML data, it is %s starting %q; use %s",
		shape, truncate(strings.TrimSpace(line), 60), tools)
}
