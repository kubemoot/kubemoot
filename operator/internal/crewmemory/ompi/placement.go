/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kubemoot/kubemoot/operator/internal/crewmemory"
)

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_=-]`)

// placed is a fact with the path it gets in the export: <topic>/<key>, reduced
// to characters that are safe in a file name and unique ignoring case.
type placed struct {
	fact crewmemory.Fact
	path string
}

// place gives every fact a safe, unique path. The order of the input does not
// matter: facts are sorted first, so a collision is settled the same way on
// every run.
func place(facts []crewmemory.Fact) []placed {
	sorted := append([]crewmemory.Fact(nil), facts...)
	crewmemory.SortFacts(sorted)
	dirs := map[string]string{} // lower-case directory -> the spelling used
	taken := map[string]bool{}  // lower-case path
	out := make([]placed, 0, len(sorted))
	for _, f := range sorted {
		dir := safeName(f.Topic)
		if first, ok := dirs[strings.ToLower(dir)]; ok {
			dir = first
		} else {
			dirs[strings.ToLower(dir)] = dir
		}
		out = append(out, placed{fact: f, path: uniquePath(dir, safeName(f.Key), taken)})
	}
	return out
}

// safeName reduces a topic or key to the characters the crew-memory key format
// allows. An empty name becomes "_".
func safeName(s string) string {
	s = unsafeName.ReplaceAllString(strings.TrimSpace(s), "_")
	if s == "" {
		return "_"
	}
	return s
}

func uniquePath(dir, name string, taken map[string]bool) string {
	candidate := name
	for n := 2; taken[strings.ToLower(dir+"/"+candidate)]; n++ {
		candidate = fmt.Sprintf("%s-%d", name, n)
	}
	p := dir + "/" + candidate
	taken[strings.ToLower(p)] = true
	return p
}
