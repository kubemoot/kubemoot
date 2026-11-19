/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// ExportFiles is the export of one crew before it is written anywhere: read its
// facts, map them to memory objects and render every document, checked against
// the schema. A store error is returned, so an unreachable server is never
// mistaken for "no facts". The returned Export names the entries that were left
// out; the files map is relative path to content (see Files).
func ExportFiles(
	r crewmemory.EntryReader, scope crewscope.Scope, withMarkdown bool, opts Options,
) (Export, map[string][]byte, error) {
	res, err := crewmemory.ReadFacts(r, scope)
	if err != nil {
		return Export{}, nil, err
	}
	exp, err := Build(scope, res, opts)
	if err != nil {
		return Export{}, nil, err
	}
	files, err := Files(exp, withMarkdown)
	if err != nil {
		return Export{}, nil, err
	}
	return exp, files, nil
}

// ExportCrew writes the export of one crew (see ExportFiles) to dir atomically.
// dir must not exist or be empty. Nothing is written when the export fails.
func ExportCrew(
	r crewmemory.EntryReader, scope crewscope.Scope, dir string, withMarkdown bool, opts Options,
) (Export, error) {
	exp, files, err := ExportFiles(r, scope, withMarkdown, opts)
	if err != nil {
		return Export{}, err
	}
	return exp, Write(dir, files)
}
