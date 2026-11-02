package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Rows stacked from the stored calls of a key pattern carry the call that produced them, so a
// query can group, join and count per call: _call is the object key of the call, _args the
// arguments it was made with, as the runtime recorded them (JSON text), and _arg_<name> holds an
// argument that is a plain value (text, number, flag) in every stacked call and differs between
// them, such as vmid, node or namespace.
const (
	callColumn = "_call"
	argsColumn = "_args"
	argPrefix  = "_arg_"
	// maxArgColumns bounds the argument columns of one table.
	maxArgColumns = 6
	// headerArgs is the OpenTelemetry GenAI header that holds a call's arguments.
	headerArgs = "gen_ai.tool.call.arguments"
)

// unsafeColumnChars are the characters an argument name may not keep in a column name.
var unsafeColumnChars = regexp.MustCompile(`[^A-Za-z0-9_]`)

// callTag is what stacked rows are tagged with: the column names, and for each call key the
// cell values in the order of names.
type callTag struct {
	names  []string
	values map[string][]string
}

// scalarArgs returns the arguments of one call that are plain values, as text. Arguments that
// are not a JSON object (or were cut by the header size limit) give none.
func scalarArgs(args string) map[string]string {
	dec := json.NewDecoder(bytes.NewReader([]byte(args)))
	dec.UseNumber()
	var obj map[string]any
	if dec.Decode(&obj) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range obj {
		switch x := v.(type) {
		case string:
			out[k] = x
		case json.Number:
			out[k] = x.String()
		case bool:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// argColumns picks the argument names for the calls of one table: present in every call as a
// plain value with at least two distinct values, in name order, at most maxArgColumns.
func argColumns(per []map[string]string) []string {
	if len(per) < 2 {
		return nil
	}
	var names []string
	for k := range per[0] {
		if varies(k, per) {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if len(names) > maxArgColumns {
		names = names[:maxArgColumns]
	}
	return names
}

// varies reports whether every call has argument k and the values are not all the same.
func varies(k string, per []map[string]string) bool {
	first, ok := per[0][k]
	if !ok {
		return false
	}
	distinct := false
	for _, m := range per[1:] {
		v, ok := m[k]
		if !ok {
			return false
		}
		distinct = distinct || v != first
	}
	return distinct
}

// tagFor makes the columns and cell values that tag the rows of each prepared call. A column
// the data already has is not added again, so existing data is never overwritten.
func tagFor(g []prepared, existing []string) callTag {
	per := make([]map[string]string, len(g))
	for i, p := range g {
		per[i] = scalarArgs(p.args)
	}
	have := map[string]bool{}
	for _, c := range existing {
		have[strings.ToLower(c)] = true
	}
	var names []string
	for _, n := range []string{callColumn, argsColumn} {
		if !have[n] {
			names = append(names, n)
			have[n] = true
		}
	}
	argOf := map[string]string{} // column to the argument name
	for _, a := range argColumns(per) {
		col := argPrefix + unsafeColumnChars.ReplaceAllString(a, "_")
		if !have[strings.ToLower(col)] {
			names = append(names, col)
			argOf[col] = a
			have[strings.ToLower(col)] = true
		}
	}
	tag := callTag{names: names, values: map[string][]string{}}
	for i, p := range g {
		cells := make([]string, len(names))
		for j, n := range names {
			cells[j] = cellFor(n, p, per[i], argOf)
		}
		tag.values[p.call] = cells
	}
	return tag
}

// cellFor is the value of column n for the call p.
func cellFor(n string, p prepared, args map[string]string, argOf map[string]string) string {
	switch n {
	case callColumn:
		return p.call
	case argsColumn:
		return p.args
	}
	return args[argOf[n]]
}
