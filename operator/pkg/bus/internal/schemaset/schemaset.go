/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package schemaset loads the bus contract's schema directory: the AsyncAPI
// document and the JSON Schema payload files beside it. The generator, the contract
// tests and the compatibility check all read the schema through it.
package schemaset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// AsyncAPIFile is the AsyncAPI document of the contract.
const AsyncAPIFile = "asyncapi.yaml"

// Set is the content of a schema directory.
type Set struct {
	// Version is the contract version, the AsyncAPI document's info.version.
	Version string
	// Digest identifies the directory's content; generated code records it.
	Digest  string
	schemas map[string]map[string]any
	raw     map[string][]byte
}

// Load reads every JSON Schema file and the AsyncAPI document in dir.
func Load(dir string) (*Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	set := &Set{schemas: map[string]map[string]any{}, raw: map[string][]byte{}}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = data
		if err := set.add(e.Name(), data); err != nil {
			return nil, err
		}
	}
	if set.Version == "" {
		return nil, fmt.Errorf("%s: no %s with info.version", dir, AsyncAPIFile)
	}
	set.Digest = Digest(files)
	return set, nil
}

// add records one file of the directory.
func (s *Set) add(name string, data []byte) error {
	if name == AsyncAPIFile {
		v, err := asyncAPIVersion(data)
		s.Version = v
		return err
	}
	if !strings.HasSuffix(name, ".json") {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	s.schemas[name] = doc
	s.raw[name] = data
	return nil
}

// asyncAPIVersion reads info.version from an AsyncAPI document.
func asyncAPIVersion(data []byte) (string, error) {
	var doc struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("%s: %w", AsyncAPIFile, err)
	}
	if doc.Info.Version == "" {
		return "", errors.New(AsyncAPIFile + ": info.version is empty")
	}
	return doc.Info.Version, nil
}

// Names lists the JSON Schema files, sorted.
func (s *Set) Names() []string {
	names := make([]string, 0, len(s.schemas))
	for n := range s.schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Schema is the parsed JSON Schema file name, or nil.
func (s *Set) Schema(name string) map[string]any {
	return s.schemas[name]
}

// Raw is the content of the JSON Schema file name, or nil.
func (s *Set) Raw(name string) []byte {
	return s.raw[name]
}

// IsComposition reports whether doc only combines other schemas with allOf and
// declares no properties of its own.
func IsComposition(doc map[string]any) bool {
	_, hasAllOf := doc["allOf"]
	_, hasProps := doc["properties"]
	return hasAllOf && !hasProps
}

// Digest is the SHA-256 of the files, each as its name, a NUL, its content and a
// NUL, in name order, in hex. The dashboard's type generator computes the same.
func Digest(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(files[n])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Marshal encodes a schema as indented JSON.
func Marshal(doc any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
