/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"strings"
	"testing"
)

const (
	crewName    = "hello"
	ctxName     = "kind-chaos"
	flagCrew    = "--crew"
	flagContext = "--context"
)

func TestParseOptionsRequiresAnExplicitContextAndCrew(t *testing.T) {
	for _, args := range [][]string{
		{},
		{flagCrew, crewName},
		{flagContext, ctxName},
		{flagContext, ctxName, flagCrew, crewName, "--mode", "random"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestParseOptionsDefaults(t *testing.T) {
	o, err := parseOptions([]string{flagContext, ctxName, flagCrew, crewName})
	if err != nil {
		t.Fatal(err)
	}
	if o.namespace != crewName || o.agent != "hello-coordinator" || o.mode != "protected" || o.natsURL != "" {
		t.Fatalf("%+v", o)
	}
	if !strings.HasPrefix(o.debugImage, "natsio/nats-box:") {
		t.Fatalf("signal image %q", o.debugImage)
	}
}

func TestSelectCasesByMode(t *testing.T) {
	o, _ := parseOptions([]string{flagContext, "c", flagCrew, "x", "--mode", "control"})
	cs, err := selectCases(o)
	if err != nil || len(cs) != 3 {
		t.Fatalf("%d cases, %v", len(cs), err)
	}
	o, _ = parseOptions([]string{flagContext, "c", flagCrew, "x", "--cases", "kill@thread_opened,baseline"})
	if cs, err = selectCases(o); err != nil || len(cs) != 2 {
		t.Fatalf("%d cases, %v", len(cs), err)
	}
}
