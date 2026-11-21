/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

import (
	"errors"
	"sort"
	"sync"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/ompi"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// memoryPart is the memory part of one conversation, read before anything is written.
type memoryPart struct {
	files   map[string][]byte
	part    Part
	leftOut []LeftOut
}

// memoryOf reads the crew's facts that name one of the conversation's threads as the
// place they were learned, and renders them as the same OMPI files the memory export
// writes. The screening is the memory export's: a fact that holds a credential is left
// out and listed by key or opaque id, never by its text. A fact from any other
// conversation, or with no recorded thread, is not read into the export.
func (b *Builder) memoryOf(p *prepared, mem *memoryRead) memoryPart {
	part := Part{Path: DirMemory + "/"}
	if b.Memory == nil {
		part.Status, part.Reason = StatusNotKept, "this installation offers no memory export"
		return memoryPart{part: part}
	}
	exp, err := b.conversationFacts(p, mem)
	if err != nil {
		return memoryUnavailable(part)
	}
	left := make([]LeftOut, 0, len(exp.Skipped))
	for _, o := range exp.Skipped {
		left = append(left, LeftOut{Key: o.Key, Reason: o.Reason})
	}
	if len(exp.Memories) == 0 && len(left) == 0 {
		part.Status = StatusEmpty
		part.Reason = "no crew memory fact records this conversation as where it was learned"
		return memoryPart{part: part}
	}
	files, err := ompi.Files(exp, false)
	if err != nil {
		return memoryUnavailable(part)
	}
	part.Status, part.Count = StatusIncluded, len(exp.Memories)
	return memoryPart{files: files, part: part, leftOut: left}
}

// memoryRead is the crew's facts, read once for a whole request: a bundle of many
// conversations reads the memory bucket one time, not once per conversation.
type memoryRead struct {
	once sync.Once
	res  crewmemory.ReadResult
	err  error
}

func (m *memoryRead) read(b *Builder, scope crewscope.Scope) (crewmemory.ReadResult, error) {
	m.once.Do(func() {
		reader, err := b.Memory()
		if err != nil || reader == nil {
			m.err = errors.New("the crew memory store is not available")
			return
		}
		m.res, m.err = crewmemory.ReadFacts(reader, scope)
	})
	return m.res, m.err
}

// conversationFacts maps the facts learned in the conversation's threads to an OMPI export,
// which screens out a fact that holds a credential.
func (b *Builder) conversationFacts(p *prepared, mem *memoryRead) (ompi.Export, error) {
	res, err := mem.read(b, p.scope)
	if err != nil {
		return ompi.Export{}, err
	}
	threads := map[string]struct{}{}
	for _, t := range threadIDs(p.held.Turns) {
		threads[t] = struct{}{}
	}
	return ompi.Build(p.scope, crewmemory.LearnedInThreads(res, threads), ompi.Options{ExportedAt: p.now})
}

func memoryUnavailable(part Part) memoryPart {
	part.Status, part.Reason = StatusUnavailable, "the crew memory store could not be read"
	return memoryPart{part: part}
}

// writeMemory writes the OMPI files under memory/, in path order.
func writeMemory(put func(string, []byte) error, p *prepared) (Part, error) {
	names := make([]string, 0, len(p.memory.files))
	for name := range p.memory.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := put(DirMemory+"/"+name, p.memory.files[name]); err != nil {
			return p.memory.part, err
		}
	}
	return p.memory.part, nil
}
