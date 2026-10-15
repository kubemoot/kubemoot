/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"bytes"
	"fmt"
	"go/format"

	"github.com/kubemoot/kubemoot/operator/pkg/bus/internal/schemaset"
)

// extension is go-jsonschema's schema keyword for Go-specific settings.
const extension = "goJSONSchema"

// scalarTypes are the JSON types whose optional fields become plain values.
var scalarTypes = map[string]bool{"string": true, "integer": true, "number": true, "boolean": true}

// PrepareForGo returns a copy of doc with go-jsonschema settings added: every
// integer is an int64, and every property of a scalar type is a value, not a
// pointer. Settings the schema already has are kept.
func PrepareForGo(doc map[string]any) map[string]any {
	return prepareSchema(doc, false)
}

// prepareSchema copies schema s with the settings added to it and to every schema
// below it. isProperty is true when s is the schema of an object property.
func prepareSchema(s map[string]any, isProperty bool) map[string]any {
	out := schemaset.Rewrite(s, func(c schemaset.Child) map[string]any {
		return prepareSchema(c.Schema, c.IsProperty())
	})
	if ext := goSettings(s, isProperty); len(ext) > 0 {
		out[extension] = ext
	}
	return out
}

// goSettings are the go-jsonschema settings for schema s, merged over any it has.
func goSettings(s map[string]any, isProperty bool) map[string]any {
	ext := map[string]any{}
	typ, _ := s["type"].(string)
	if typ == "integer" {
		ext["type"] = "int64"
	}
	if isProperty && scalarTypes[typ] {
		ext["pointer"] = false
	}
	if own, ok := s[extension].(map[string]any); ok {
		for k, v := range own {
			ext[k] = v
		}
	}
	return ext
}

// decorate adds the contract version and the schema digest to the generated code
// and formats it.
func decorate(code []byte, set *schemaset.Set) ([]byte, error) {
	header, rest, found := bytes.Cut(code, []byte("\n"))
	if !found {
		return nil, fmt.Errorf("unexpected go-jsonschema output: %q", code)
	}
	var buf bytes.Buffer
	buf.Write(header)
	fmt.Fprintf(&buf, "\n// Schema digest: %s\n", set.Digest)
	buf.Write(rest)
	fmt.Fprintf(&buf, "\n// ContractVersion is the version of the bus contract these types describe.\n")
	fmt.Fprintf(&buf, "const ContractVersion = %q\n", set.Version)
	return format.Source(buf.Bytes())
}
