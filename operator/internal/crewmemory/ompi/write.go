/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Layout of an export directory:
//
//	manifest.json                  the manifest
//	memories/<identifier>.json     one memory object each (interchange)
//	markdown/<topic>/<key>.md      the runtime projection, only when asked for
const (
	ManifestFile = "manifest.json"
	MemoriesDir  = "memories"
	MarkdownDir  = "markdown"
)

// Files renders an export as relative path -> content, checking every JSON
// document against the schema, so a nonconforming export is never written.
func Files(exp Export, withMarkdown bool) (map[string][]byte, error) {
	files := map[string][]byte{}
	mf, err := marshalChecked(exp.Manifest, ValidateManifest)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	files[ManifestFile] = mf
	for _, m := range exp.Memories {
		data, err := marshalChecked(m, ValidateMemory)
		if err != nil {
			return nil, fmt.Errorf("memory %s: %w", m.Identifier, err)
		}
		files[MemoriesDir+"/"+m.Identifier+".json"] = data
		if withMarkdown {
			md, err := Projection(m)
			if err != nil {
				return nil, err
			}
			files[MarkdownDir+"/"+m.Path+".md"] = []byte(md)
		}
	}
	return files, nil
}

func marshalChecked(v any, check func([]byte) error) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := check(data); err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Write puts the files under root. The directory must not exist or must be
// empty: an export never mixes into, or overwrites, other files. Every path is
// checked before anything is created, and the files go into a temporary sibling
// directory that replaces root only when all of them are written, so a failure
// leaves no partial export.
func Write(root string, files map[string][]byte) error {
	for rel := range files {
		if err := checkRelative(rel); err != nil {
			return err
		}
	}
	if err := checkEmpty(root); err != nil {
		return err
	}
	parent := filepath.Dir(filepath.Clean(root))
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, ".ompi-export-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() // gone after the rename; removes a failed attempt
	if err := writeAll(tmp, files); err != nil {
		return err
	}
	if err := os.Remove(root); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replace %s: %w", root, err)
	}
	return os.Rename(tmp, root)
}

func writeAll(dir string, files map[string][]byte) error {
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

// checkEmpty accepts a missing directory or an empty one.
func checkEmpty(root string) error {
	entries, err := os.ReadDir(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", root, err)
	case len(entries) > 0:
		return fmt.Errorf("export directory %s is not empty", root)
	}
	return nil
}

// checkRelative rejects a path that could leave the export directory.
func checkRelative(rel string) error {
	if !filepath.IsLocal(rel) || strings.Contains(rel, "..") || strings.Contains(rel, `\`) {
		return fmt.Errorf("unsafe export path %q", rel)
	}
	return nil
}
