/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"bytes"
	"embed"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/memory.schema.json schema/manifest.schema.json
var schemaFiles embed.FS

const (
	memorySchemaFile   = "schema/memory.schema.json"
	manifestSchemaFile = "schema/manifest.schema.json"
)

// ValidateMemory checks one interchange memory object against Kubemoot's
// schema derived from the working spec (see schema/README.md).
func ValidateMemory(data []byte) error { return validate(memorySchemaFile, data) }

// ValidateManifest checks an export manifest against the same schema set.
func ValidateManifest(data []byte) error { return validate(manifestSchemaFile, data) }

func validate(file string, data []byte) error {
	sch, err := compile(file)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("does not conform to %s: %w", file, err)
	}
	return nil
}

func compile(file string) (*jsonschema.Schema, error) {
	raw, err := schemaFiles.ReadFile(file)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(file, doc); err != nil {
		return nil, err
	}
	return c.Compile(file)
}
