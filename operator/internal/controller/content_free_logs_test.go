/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"

	"github.com/kubemoot/kubemoot/operator/pkg/logcontent"
)

// A question and a secret-shaped string that must stay out of the operator's logs.
const (
	testQuestion = "why does the invoice for ACME-7731 fail"
	testSecret   = "sk-test0123456789abcdefABCDEF"
)

// capturedLog returns a logger at verbosity 1, so a debug line is written when the code
// chooses to write it, and the text written so far.
func capturedLog() (logr.Logger, func() string) {
	var out strings.Builder
	log := funcr.New(func(_, args string) { out.WriteString(args + "\n") }, funcr.Options{Verbosity: 1})
	return log, out.String
}

func TestUnparsedAgentResponseIsLoggedWithoutContent(t *testing.T) {
	reply := "Answer to " + testQuestion + " with key " + testSecret
	t.Setenv(logcontent.Env, "false")
	log, written := capturedLog()

	logUnparsedResponse(log, "Failed to parse agent response", errors.New("bad json"), reply)

	out := written()
	for _, content := range []string{testQuestion, testSecret} {
		if strings.Contains(out, content) {
			t.Errorf("switch off: log carries %q: %s", content, out)
		}
	}
	if !strings.Contains(out, "Failed to parse agent response") || !strings.Contains(out, logcontent.Size(reply)) {
		t.Errorf("switch off: the line should name the failure and the size, got %s", out)
	}
}

func TestUnparsedAgentResponseIsLoggedWithContentWhenOn(t *testing.T) {
	t.Setenv(logcontent.Env, "true")
	log, written := capturedLog()

	logUnparsedResponse(log, "Failed to parse agent response", errors.New("bad json"), testQuestion)

	if !strings.Contains(written(), testQuestion) {
		t.Errorf("switch on: the content line is missing: %s", written())
	}
}

func TestJudgeReasonIsLoggedWithoutContent(t *testing.T) {
	reason := "the answer quoted " + testQuestion + " and " + testSecret
	t.Setenv(logcontent.Env, "false")
	log, written := capturedLog()

	logScenarioScored(log, "scenario-1", 0.5, reason)

	out := written()
	for _, content := range []string{testQuestion, testSecret} {
		if strings.Contains(out, content) {
			t.Errorf("switch off: log carries %q: %s", content, out)
		}
	}
	if !strings.Contains(out, "scenario-1") {
		t.Errorf("the line should name the scenario: %s", out)
	}
}

func TestJudgeReasonIsLoggedWithContentWhenOn(t *testing.T) {
	t.Setenv(logcontent.Env, "true")
	log, written := capturedLog()

	logScenarioScored(log, "scenario-1", 0.5, "reason: "+testQuestion)

	if !strings.Contains(written(), testQuestion) {
		t.Errorf("switch on: the content line is missing: %s", written())
	}
}
