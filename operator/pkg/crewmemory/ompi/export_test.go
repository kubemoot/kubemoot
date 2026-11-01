/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

type mapReader map[string]string

func (r mapReader) ListKVKeys(string) ([]string, error) {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	return keys, nil
}

func (r mapReader) GetKVEntry(_, key string) (crewmemory.Entry, error) {
	return crewmemory.Entry{Value: []byte(r[key]), Revision: 2, Created: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)}, nil
}

type listFails struct{ mapReader }

func (listFails) ListKVKeys(string) ([]string, error) { return nil, errors.New("server went away") }

func TestExportCrewWritesAValidDirectory(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	dir := filepath.Join(t.TempDir(), "memory")
	r := mapReader{"ns.c.hardware.gpu0": `{"value":"RTX 5090","learnedBy":"a"}`, "ns.c.hardware.bad": `nope`}
	exp, err := ExportCrew(r, scope, dir, true, Options{ExportedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Memories) != 1 || len(exp.Skipped) != 1 {
		t.Errorf("memories = %d, skipped = %d, want 1 and 1", len(exp.Memories), len(exp.Skipped))
	}
	for _, f := range []string{"manifest.json", "markdown/hardware/gpu0.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
}

func TestExportCrewWritesNothingWhenTheStoreFails(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	dir := filepath.Join(t.TempDir(), "memory")
	if _, err := ExportCrew(listFails{}, scope, dir, false, Options{ExportedAt: time.Now()}); err == nil {
		t.Fatal("ExportCrew() = nil error, want the store error")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a directory was created although the store failed")
	}
}
