/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
)

const gpuKey = "ns.c.hardware.gpu0"

type failingReader struct{ staticReader }

func (failingReader) ListKVKeys(string) ([]string, error) { return nil, errors.New("server went away") }

func openReader(r crewmemory.EntryReader) opener {
	return func(options) (crewmemory.EntryReader, func(), error) { return r, func() {}, nil }
}

func TestRunFailsWhenTheStoreCannotBeListed(t *testing.T) {
	out := filepath.Join(t.TempDir(), "memory")
	err := runWith([]string{nsFlag, "ns", crewFlag, "c", outFlag, out}, io.Discard, openReader(failingReader{}))
	if err == nil || !strings.Contains(err.Error(), "server went away") {
		t.Fatalf("runWith() = %v, want the store error", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("an export directory was created although the store failed")
	}
}

func TestRunWritesNoMarkdownUnlessAsked(t *testing.T) {
	r := staticReader{gpuKey: `{"value":"RTX 5090","learnedAt":"2026-10-30T10:00:00Z"}`}
	out := filepath.Join(t.TempDir(), "memory")
	if err := runWith([]string{nsFlag, "ns", crewFlag, "c", outFlag, out}, io.Discard, openReader(r)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "markdown")); err == nil {
		t.Error("markdown projection written without --markdown")
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Errorf("manifest missing: %v", err)
	}
}

func TestRunReportsEntriesItLeftOut(t *testing.T) {
	r := staticReader{
		gpuKey:              `{"value":"RTX 5090"}`,
		"ns.c.hardware.bad": `not json`,
	}
	var stdout strings.Builder
	out := filepath.Join(t.TempDir(), "memory")
	if err := runWith([]string{nsFlag, "ns", crewFlag, "c", outFlag, out}, &stdout, openReader(r)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `left out "ns.c.hardware.bad"`) {
		t.Errorf("stdout = %q, want the left-out entry named", stdout.String())
	}
	manifest, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), `"selection": "selection"`) {
		t.Errorf("manifest = %s, err = %v", manifest, err)
	}
}

func TestRunRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runWith([]string{nsFlag, "ns", crewFlag, "c", outFlag, dir}, io.Discard, openReader(staticReader{})); err == nil {
		t.Error("runWith() = nil, want a non-empty directory error")
	}
}
