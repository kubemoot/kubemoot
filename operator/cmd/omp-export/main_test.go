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

	"github.com/kubemoot/kubemoot/operator/internal/crewmemory"
)

const (
	outFlag  = "--out"
	nsFlag   = "--namespace"
	crewFlag = "--crew"
)

func TestParseFlagsRequiresScopeAndOutput(t *testing.T) {
	for _, args := range [][]string{
		{},
		{nsFlag, "ns", crewFlag, "c"},
		{nsFlag, "ns", outFlag, "o"},
		{crewFlag, "c", outFlag, "o"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) = nil error, want required-flag error", args)
		}
	}
}

func TestParseFlagsAcceptsFullSet(t *testing.T) {
	o, err := parseFlags([]string{nsFlag, "ns", crewFlag, "c", outFlag, "o", "--nats-url", "nats://x:1"})
	if err != nil || o.namespace != "ns" || o.crew != "c" || o.out != "o" || o.natsURL != "nats://x:1" {
		t.Errorf("parseFlags() = %+v, %v", o, err)
	}
}

type staticReader map[string]string

func (r staticReader) ListKVKeys(string) ([]string, error) {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	return keys, nil
}

func (r staticReader) GetKVValue(_, key string) ([]byte, error) { return []byte(r[key]), nil }

func TestRunWritesValidExportFromReader(t *testing.T) {
	r := staticReader{
		"ns.c.hardware.gpu0": `{"value":"RTX 5090","learnedBy":"a"}`,
		"ns.c.hardware.bad":  `not json`,
		"ns.other.t.k":       `{"value":"x"}`,
	}
	open := func(options) (crewmemory.Reader, func(), error) { return r, func() {}, nil }
	out := filepath.Join(t.TempDir(), "memory")
	var stdout strings.Builder
	if err := runWith([]string{nsFlag, "ns", crewFlag, "c", outFlag, out}, &stdout, open); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "wrote 1 fact(s)") {
		t.Errorf("stdout = %q", stdout.String())
	}
	for _, f := range []string{"MEMORY.md", "hardware/MEMORY.md", "hardware/gpu0.md"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
}

func TestRunReportsConnectionFailure(t *testing.T) {
	open := func(options) (crewmemory.Reader, func(), error) { return nil, nil, errors.New("no server") }
	err := runWith([]string{nsFlag, "ns", crewFlag, "c", outFlag, t.TempDir()}, io.Discard, open)
	if err == nil || !strings.Contains(err.Error(), "no server") {
		t.Errorf("runWith() = %v", err)
	}
}

func TestRunRejectsDottedCrewName(t *testing.T) {
	err := run([]string{nsFlag, "ns", crewFlag, "a.b", outFlag, t.TempDir()}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "crew") {
		t.Errorf("run() = %v, want a crew token error", err)
	}
}
