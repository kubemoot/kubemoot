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
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

const (
	gpuKey   = "ns.c.hardware.gpu0"
	loginKey = "ns.c.ops.login"

	gpuFact   = `{"value":"RTX 5090","learnedBy":"a"}`
	loginFact = `{"value":"Authorization: Bearer abcdefgh12345678","learnedBy":"a"}`
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
	r := mapReader{gpuKey: gpuFact, "ns.c.hardware.bad": `nope`}
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

func TestExportCrewKeepsACredentialOutOfTheFiles(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	dir := filepath.Join(t.TempDir(), "memory")
	r := mapReader{
		gpuKey:   gpuFact,
		loginKey: loginFact,
	}
	exp, err := ExportCrew(r, scope, dir, true, Options{ExportedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Memories) != 1 || len(exp.Skipped) != 1 {
		t.Fatalf("memories = %d, skipped = %d, want 1 and 1", len(exp.Memories), len(exp.Skipped))
	}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "abcdefgh12345678") {
			t.Errorf("%s holds the credential", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExportFilesReturnsTheFilesAndTheLeftOutEntriesWithoutWriting(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	r := mapReader{
		gpuKey:   gpuFact,
		loginKey: loginFact,
	}
	exp, files, err := ExportFiles(r, scope, false, Options{ExportedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Memories) != 1 || len(exp.Skipped) != 1 || len(files) != 2 {
		t.Fatalf("memories = %d, skipped = %d, files = %d, want 1, 1 and 2", len(exp.Memories), len(exp.Skipped), len(files))
	}
	for path, content := range files {
		if strings.Contains(string(content), "abcdefgh12345678") {
			t.Errorf("%s holds the credential", path)
		}
	}
}

func TestExportFilesWithAllowUnscreenedKeepsTheCredentialFact(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	r := mapReader{loginKey: loginFact}
	exp, _, err := ExportFiles(r, scope, false, Options{ExportedAt: time.Now(), AllowUnscreened: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Memories) != 1 || len(exp.Skipped) != 0 {
		t.Errorf("memories = %d, skipped = %d, want 1 and 0", len(exp.Memories), len(exp.Skipped))
	}
}

func TestExportFilesReturnsTheStoreError(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	if _, _, err := ExportFiles(listFails{}, scope, false, Options{ExportedAt: time.Now()}); err == nil {
		t.Fatal("ExportFiles() = nil error, want the store error")
	}
}
