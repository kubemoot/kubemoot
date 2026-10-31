/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package omp

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// ValidateTree applies the structural rules of the OMP loader/validator to a
// tree: the root has a MEMORY.md, and every directory holding files has one.
// It mirrors the published Python validator so the export is checked on every
// test run; the Python validator itself is run by scripts/omp-validate.sh.
func ValidateTree(tree Tree) error {
	if _, ok := tree[IndexFile]; !ok {
		return fmt.Errorf("missing required root %s", IndexFile)
	}
	dirs := map[string]bool{}
	for rel := range tree {
		if !strings.HasSuffix(rel, ".md") {
			return fmt.Errorf("%s is not a markdown file", rel)
		}
		dirs[path.Dir(rel)] = true
	}
	var missing []string
	for dir := range dirs {
		if _, ok := tree[path.Join(dir, IndexFile)]; !ok {
			missing = append(missing, dir)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("directories without %s: %s", IndexFile, strings.Join(missing, ", "))
	}
	return nil
}
