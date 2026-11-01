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

// ExportCrew is the whole export of one crew: read its facts, map them to memory
// objects, check every document against the schema and write the directory
// atomically. dir must not exist or be empty. A store error is returned and
// nothing is written, so an unreachable server is never mistaken for "no facts".
// The returned Export names the entries that were left out.
func ExportCrew(
	r crewmemory.EntryReader, scope crewscope.Scope, dir string, withMarkdown bool, opts Options,
) (Export, error) {
	res, err := crewmemory.ReadFacts(r, scope)
	if err != nil {
		return Export{}, err
	}
	exp, err := Build(scope, res, opts)
	if err != nil {
		return Export{}, err
	}
	files, err := Files(exp, withMarkdown)
	if err != nil {
		return Export{}, err
	}
	return exp, Write(dir, files)
}
