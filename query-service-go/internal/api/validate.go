package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

// locBody is the first element of every validation error location.
const locBody = "body"

// Validation error types for a bound the value crosses.
const (
	typeGreaterEqual = "greater_than_equal"
	typeLessEqual    = "less_than_equal"
)

// Bounds the Python service's request model set with pydantic.
const (
	minThreshold = 0.0
	maxThreshold = 1.0
)

// QueryRequest is a validated query body. TopK and Threshold are nil when the caller
// sent none (or null). The body's "filter" is validated and then ignored, as before.
type QueryRequest struct {
	Query     string
	TopK      *int
	Threshold *float64
}

// A JSON syntax error reports the error text of Go's decoder and a byte offset, where
// pydantic reports its parser's text and position; only the type and the body location match.
// FieldError is one validation failure in the shape FastAPI's 422 answer uses, so a
// caller that parses {"detail":[{"loc","msg","type"}]} keeps working. type and loc
// match; msg wording follows pydantic v2 for the cases below.
type FieldError struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

// ParseQuery validates a request body. It reports every failing field, like pydantic,
// in the order query, top_k, threshold, filter.
func ParseQuery(body []byte) (QueryRequest, []FieldError) {
	if len(bytes.TrimSpace(body)) == 0 {
		return QueryRequest{}, []FieldError{{
			Type: "missing", Loc: []any{locBody}, Msg: "Field required", Input: nil,
		}}
	}
	doc, err := decodeJSON(body)
	if err != nil {
		return QueryRequest{}, []FieldError{jsonInvalid(err)}
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return QueryRequest{}, []FieldError{{
			Type: "model_attributes_type", Loc: []any{locBody}, Input: doc,
			Msg: "Input should be a valid dictionary or object to extract fields from",
		}}
	}
	return checkFields(obj)
}

func checkFields(obj map[string]any) (QueryRequest, []FieldError) {
	var (
		req  QueryRequest
		errs []FieldError
	)
	add := func(e *FieldError) {
		if e != nil {
			errs = append(errs, *e)
		}
	}
	add(checkQuery(obj, &req))
	add(checkTopK(obj, &req))
	add(checkThreshold(obj, &req))
	add(checkFilter(obj))
	return req, errs
}

func jsonInvalid(err error) FieldError {
	var pos int64
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		pos = syn.Offset
	}
	return FieldError{
		Type: "json_invalid", Loc: []any{locBody, pos}, Msg: "JSON decode error",
		Input: map[string]any{}, Ctx: map[string]any{"error": err.Error()},
	}
}

func fieldErr(field, typ, msg string, input any, ctx map[string]any) *FieldError {
	return &FieldError{Type: typ, Loc: []any{locBody, field}, Msg: msg, Input: input, Ctx: ctx}
}

func checkQuery(obj map[string]any, req *QueryRequest) *FieldError {
	v, present := obj["query"]
	if !present {
		return fieldErr("query", "missing", "Field required", obj, nil)
	}
	s, ok := v.(string)
	if !ok {
		return fieldErr("query", "string_type", "Input should be a valid string", v, nil)
	}
	req.Query = s
	return nil
}

func checkTopK(obj map[string]any, req *QueryRequest) *FieldError {
	v := obj["top_k"]
	if v == nil {
		return nil
	}
	n, problem := validateTopK(v)
	if problem != nil {
		return problem
	}
	req.TopK = &n
	return nil
}

// validateTopK reads a top_k value and checks its bounds; the /query body and the MCP
// tool's arguments both go through it.
func validateTopK(v any) (int, *FieldError) {
	n, e := asInt(v)
	if e != nil {
		return 0, fieldErr("top_k", e.typ, e.msg, v, nil)
	}
	if n < registry.MinTopK {
		return 0, fieldErr("top_k", typeGreaterEqual,
			fmt.Sprintf("Input should be greater than or equal to %d", registry.MinTopK),
			v, map[string]any{"ge": registry.MinTopK})
	}
	if n > registry.MaxTopK {
		return 0, fieldErr("top_k", typeLessEqual,
			fmt.Sprintf("Input should be less than or equal to %d", registry.MaxTopK),
			v, map[string]any{"le": registry.MaxTopK})
	}
	return n, nil
}

func checkThreshold(obj map[string]any, req *QueryRequest) *FieldError {
	v := obj["threshold"]
	if v == nil {
		return nil
	}
	f, p := asFloat(v)
	if p != nil || math.IsNaN(f) {
		if p == nil {
			p = &intProblem{"float_parsing", "Input should be a valid number, unable to parse string as a number"}
		}
		return fieldErr("threshold", p.typ, p.msg, v, nil)
	}
	if f < minThreshold {
		return fieldErr("threshold", typeGreaterEqual,
			"Input should be greater than or equal to 0", v, map[string]any{"ge": minThreshold})
	}
	if f > maxThreshold {
		return fieldErr("threshold", typeLessEqual,
			"Input should be less than or equal to 1", v, map[string]any{"le": maxThreshold})
	}
	req.Threshold = &f
	return nil
}

func checkFilter(obj map[string]any) *FieldError {
	v := obj["filter"]
	if v == nil {
		return nil
	}
	if _, ok := v.(map[string]any); !ok {
		return fieldErr("filter", "dict_type", "Input should be a valid dictionary", v, nil)
	}
	return nil
}

type intProblem struct{ typ, msg string }

// What a value that is not a whole number reads as, by where it came from.
var (
	intFromFloat = intProblem{"int_from_float", "Input should be a valid integer, got a number with a fractional part"}
	intParsing   = intProblem{"int_parsing", "Input should be a valid integer, unable to parse string as an integer"}
)

// asInt reads an integer the way pydantic's lax mode does, which is what the Python
// service accepted: a whole number written as 5 or 5.0, a string holding one, or a
// boolean.
func asInt(v any) (int, *intProblem) {
	switch x := v.(type) {
	case json.Number:
		return intFromNumber(x.String(), intFromFloat)
	case string:
		return intFromNumber(strings.TrimSpace(x), intParsing)
	case bool:
		return boolInt(x), nil
	}
	return 0, &intProblem{"int_type", "Input should be a valid integer"}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// intFromNumber parses text as a whole number; a fraction or garbage fails with the given
// type and message.
func intFromNumber(text string, bad intProblem) (int, *intProblem) {
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return clampInt(n), nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || f != math.Trunc(f) {
		return 0, &bad
	}
	return clampInt(int64(math.Max(math.Min(f, math.MaxInt32), math.MinInt32))), nil
}

// asFloat reads a number the way pydantic's lax mode does: a JSON number, a string
// holding one, or a boolean.
func asFloat(v any) (float64, *intProblem) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, floatProblem(err, "float_type", "Input should be a valid number")
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, floatProblem(err, "float_parsing", "Input should be a valid number, unable to parse string as a number")
	case bool:
		return float64(boolInt(x)), nil
	}
	return 0, &intProblem{"float_type", "Input should be a valid number"}
}

func floatProblem(err error, typ, msg string) *intProblem {
	if err != nil {
		return &intProblem{typ, msg}
	}
	return nil
}

// clampInt keeps an out-of-range whole number out of int overflow; it still fails the
// bounds check.
func clampInt(n int64) int {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	}
	return int(n)
}

// decodeJSON reads exactly one JSON value, numbers kept as json.Number so integers stay exact.
func decodeJSON(body []byte) (any, error) {
	var doc any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("extra data after the JSON value")
	}
	return doc, nil
}
