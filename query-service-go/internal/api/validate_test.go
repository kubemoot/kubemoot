package api

import (
	"reflect"
	"testing"
)

func TestParseQueryAcceptsAFullBody(t *testing.T) {
	req, errs := ParseQuery([]byte(`{"query":"hello","top_k":5,"threshold":0.25,"filter":{"x":[1]}}`))
	if len(errs) != 0 || req.Query != "hello" || *req.TopK != 5 || *req.Threshold != 0.25 {
		t.Fatalf("%+v %+v", req, errs)
	}
}

func TestParseQueryAcceptsEdgeValues(t *testing.T) {
	req, errs := ParseQuery([]byte(`{"query":"","top_k":5.0,"threshold":1,"filter":null}`))
	if len(errs) != 0 || req.Query != "" || *req.TopK != 5 || *req.Threshold != 1 {
		t.Fatalf("an empty query, a whole-number float and an integer threshold are valid: %+v %+v", req, errs)
	}
}

func TestParseQueryIgnoresNullsAndUnknownKeys(t *testing.T) {
	req, errs := ParseQuery([]byte(`{"query":"q","top_k":null,"threshold":null,"extra":true}`))
	if len(errs) != 0 || req.TopK != nil || req.Threshold != nil {
		t.Fatalf("%+v %+v", req, errs)
	}
}

func TestParseQueryBoundaries(t *testing.T) {
	ok := []string{`"top_k":1`, `"top_k":"5"`, `"top_k":" 5 "`, `"top_k":"5.0"`, `"top_k":1e2`, `"top_k":true`, `"threshold":"0.5"`, `"threshold":true`, `"top_k":100`, `"threshold":0`, `"threshold":0.0`, `"threshold":1.0`}
	for _, f := range ok {
		if _, errs := ParseQuery([]byte(`{"query":"q",` + f + `}`)); len(errs) != 0 {
			t.Errorf("%s: %+v", f, errs)
		}
	}
	bad := map[string]string{
		`"top_k":0`:         typeGTE,
		`"top_k":-3`:        typeGTE,
		`"top_k":101`:       typeLTE,
		`"top_k":1e30`:      typeLTE,
		`"top_k":2.5`:       "int_from_float",
		`"top_k":"abc"`:     "int_parsing",
		`"top_k":"2.5"`:     "int_parsing",
		`"top_k":[1]`:       "int_type",
		`"top_k":{}`:        "int_type",
		`"top_k":false`:     typeGTE,
		`"threshold":-0.1`:  typeGTE,
		`"threshold":1.01`:  typeLTE,
		`"threshold":"abc"`: "float_parsing",
		`"threshold":[1]`:   "float_type",
		`"threshold":{}`:    "float_type",
		`"filter":"x"`:      typeDict,
		`"filter":[1]`:      typeDict,
	}
	for field, wantType := range bad {
		_, errs := ParseQuery([]byte(`{"query":"q",` + field + `}`))
		if len(errs) != 1 || errs[0].Type != wantType {
			t.Errorf("%s: want %s, got %+v", field, wantType, errs)
		}
	}
}

func TestParseQueryReportsEveryFailureInFieldOrder(t *testing.T) {
	_, errs := ParseQuery([]byte(`{"top_k":0,"threshold":9,"filter":1}`))
	got := make([][]any, 0, len(errs))
	for _, e := range errs {
		got = append(got, append([]any{e.Type}, e.Loc...))
	}
	want := [][]any{
		{"missing", locBody, fieldQuery}, {typeGTE, locBody, "top_k"},
		{typeLTE, locBody, "threshold"}, {typeDict, locBody, "filter"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestParseQueryStructuralFailures(t *testing.T) {
	cases := map[string]string{
		`{`:               typeJSON,
		`nope`:            typeJSON,
		`[]`:              typeModel,
		`"text"`:          typeModel,
		`null`:            typeModel,
		`{"query":null}`:  "string_type",
		`{"query":"q"} x`: typeJSON,
		`{"query":5}`:     "string_type",
	}
	for body, wantType := range cases {
		_, errs := ParseQuery([]byte(body))
		if len(errs) != 1 || errs[0].Type != wantType {
			t.Errorf("%q: want %s, got %+v", body, wantType, errs)
		}
	}
}

func TestParseQueryAnEmptyBodyIsAMissingBody(t *testing.T) {
	for _, body := range []string{``, "  \n"} {
		_, errs := ParseQuery([]byte(body))
		if len(errs) != 1 || errs[0].Type != "missing" || len(errs[0].Loc) != 1 || errs[0].Loc[0] != locBody {
			t.Errorf("%q: %+v", body, errs)
		}
	}
}
