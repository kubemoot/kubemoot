/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package omp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Write puts the tree under root. The directory must not exist or must be
// empty: an export never mixes into, or overwrites, other files.
func Write(root string, tree Tree) error {
	for rel := range tree {
		if err := checkRelative(rel); err != nil {
			return err
		}
	}
	if err := requireEmpty(root); err != nil {
		return err
	}
	for rel, content := range tree {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func requireEmpty(root string) error {
	entries, err := os.ReadDir(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return os.MkdirAll(root, 0o750)
	case err != nil:
		return fmt.Errorf("read %s: %w", root, err)
	case len(entries) > 0:
		return fmt.Errorf("export directory %s is not empty", root)
	}
	return nil
}

// checkRelative rejects a path that could leave the export directory.
func checkRelative(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") || strings.Contains(rel, `\`) {
		return fmt.Errorf("unsafe export path %q", rel)
	}
	return nil
}
