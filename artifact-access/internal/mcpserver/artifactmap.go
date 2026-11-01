package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// artifactsShape is the shape workspace_query expects, shown in every error about it.
const artifactsShape = `{"<table>": "<artifact key>"}`

// artifactMap maps a table name to an artifact object key. Models often send a
// nested object as a JSON-encoded string, so it decodes from either an object or a
// string holding that object. Only workspace_query's artifacts parameter uses it.
type artifactMap map[string]string

// UnmarshalJSON accepts a JSON object of string values, or a JSON string whose
// content is such an object.
func (a *artifactMap) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(trimmed, &inner); err != nil {
			return fmt.Errorf("artifacts: %w", err)
		}
		trimmed = bytes.TrimSpace([]byte(inner))
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return fmt.Errorf("artifacts as a string must hold a JSON object like %s, got %q", artifactsShape, inner)
		}
	}
	var m map[string]string
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return fmt.Errorf("artifacts must be an object like %s with string values: %w", artifactsShape, err)
	}
	*a = m
	return nil
}

// queryInputSchema is workspace_query's published input schema: the schema derived
// from queryIn, with only artifacts widened to an object or a string holding that
// object, so the validator accepts the form the handler decodes.
func queryInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[queryIn](nil)
	if err != nil {
		panic(fmt.Errorf("workspace_query input schema: %w", err))
	}
	artifacts := schema.Properties["artifacts"]
	artifacts.Type = ""
	artifacts.Types = []string{"object", "string"}
	artifacts.Description = "table name you choose, mapped to the artifact key, e.g. " + artifactsShape +
		"; a string holding that JSON object is also accepted"
	return schema
}
