/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command gen generates the Go types of the bus contract from the JSON Schema files
// in ../schema with go-jsonschema. Before generating, it marks every optional scalar
// field as a plain value (not a pointer) and every integer as int64, so the types
// read like hand-written encoding/json structs: an absent field is the zero value
// and is left out again on encoding.
//
// Run it with go generate from the bus package directory.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kubemoot/kubemoot/operator/pkg/bus/internal/schemaset"
)

// goJSONSchema is the go-jsonschema release the types are generated with.
const goJSONSchema = "github.com/atombender/go-jsonschema@v0.24.1"

// output is the generated file, in the bus package directory.
const output = "zz_generated.bus.go"

// initialisms keep their capitals in generated names, as Go names write them.
var initialisms = []string{"ID", "URL", "MiB", "GPU"}

func main() {
	if err := run("schema", output); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

// run generates outFile from the schema files in schemaDir.
func run(schemaDir, outFile string) error {
	set, err := schemaset.Load(schemaDir)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "bus-schema-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	files, err := writeForGo(set, tmp)
	if err != nil {
		return err
	}
	code, err := generate(files)
	if err != nil {
		return err
	}
	orders, err := propertyOrders(set)
	if err != nil {
		return err
	}
	src, err := decorate([]byte(ReorderFields(string(code), orders)), set)
	if err != nil {
		return err
	}
	return os.WriteFile(outFile, src, 0o644)
}

// writeForGo writes each payload schema, prepared for go-jsonschema, into dir and
// returns their paths. Schemas made only of allOf are left out: go-jsonschema makes
// no type for them, and Go code embeds their parts instead.
func writeForGo(set *schemaset.Set, dir string) ([]string, error) {
	var files []string
	for _, name := range set.Names() {
		doc := set.Schema(name)
		if schemaset.IsComposition(doc) {
			continue
		}
		data, err := schemaset.Marshal(PrepareForGo(doc))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	return files, nil
}

// propertyOrders collects the property order of every titled schema in set.
func propertyOrders(set *schemaset.Set) (map[string][]string, error) {
	all := map[string][]string{}
	for _, name := range set.Names() {
		orders, err := PropertyOrders(set.Raw(name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for title, order := range orders {
			all[title] = order
		}
	}
	return all, nil
}

// generate runs go-jsonschema over files and returns the generated source.
func generate(files []string) ([]byte, error) {
	flags := []string{
		"run", goJSONSchema, "--only-models", "--struct-name-from-title",
		"--package", "bus", "--tags", "json", "--disable-omitzero",
	}
	args := make([]string, 0, len(flags)+2*len(initialisms)+len(files))
	args = append(args, flags...)
	for _, word := range initialisms {
		args = append(args, "--capitalization", word)
	}
	args = append(args, files...)
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go-jsonschema: %w", err)
	}
	return out, nil
}
