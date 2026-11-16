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
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/evidencenames"
	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

// The two buckets the discussion workspace lives in: the named one and the one
// the agents still write to. Both are read, as the workspace readers do.
const (
	workspaceBucket       = "kubemoot_workspace"
	legacyWorkspaceBucket = "kubemoot_discussion_artifacts"
)

// Header names the agent runtime sets on a stored tool call.
const (
	headerOperation = "gen_ai.operation.name"
	operationTool   = "execute_tool"
	headerArguments = "gen_ai.tool.call.arguments"
	headerTruncated = "kubemoot.truncated"
	headerType      = "Content-Type"
	headerEncoded   = "utf8-pct:"
)

var (
	// callKeyTail matches the last segment of a tool-call object: {nnn}-{tool}.
	callKeyTail = regexp.MustCompile(`^[0-9]{3,}-`)
	// callLine matches the key of a [CALL key=...] line of a finding, relative to the thread.
	callLine = regexp.MustCompile(`\[CALL key=([^\s\]]+)`)
)

// Evidence is the outcome of the invented-name check, stored in the transcript.
type Evidence struct {
	// Captured is true when every tool result of the thread was read, so a name
	// missing from them is missing from the run's evidence. When false the check
	// did not run and InventedNames is empty.
	Captured bool `json:"evidenceCaptured,omitempty"`
	// Calls is how many tool results were read.
	Calls int `json:"evidenceCalls,omitempty"`
	// InventedNames are the names the answer asserts that no tool result held.
	InventedNames []string `json:"inventedNames,omitempty"`
}

// evidenceStore is the slice of a NATS object store the capture reads.
type evidenceStore interface {
	List(opts ...nats.ListObjectsOpt) ([]*nats.ObjectInfo, error)
	GetBytes(name string, opts ...nats.GetObjectOpt) ([]byte, error)
}

// headerValue is the first value of a header, matched without regard to case:
// the runtime writes lower-case names and a Go header map canonicalizes them.
func headerValue(h nats.Header, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// inThread is true for a stored tool call of the thread: its key is
// {namespace}/{crew}/{threadId}/{agent}/{nnn}-{tool} and its headers name it an
// execute_tool operation.
func inThread(info *nats.ObjectInfo, namespace, threadID string) bool {
	if info == nil || info.Deleted {
		return false
	}
	parts := strings.Split(info.Name, "/")
	if len(parts) < 5 || parts[0] != namespace || parts[2] != threadID {
		return false
	}
	if !callKeyTail.MatchString(parts[len(parts)-1]) {
		return false
	}
	return headerValue(info.Headers, headerOperation) == operationTool
}

// relativeKey is a call's key relative to the thread: {agent}/{nnn}-{tool}.
func relativeKey(name string) string {
	parts := strings.SplitN(name, "/", 4)
	if len(parts) < 4 {
		return name
	}
	return parts[3]
}

// expectedCallKeys are the keys of the [CALL key=...] lines of the stream's
// findings: the calls the run says it made.
func expectedCallKeys(events []SignalEvent) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		for _, m := range callLine.FindAllStringSubmatch(e.Content, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// decodeHeader returns the text of a header value written with the runtime's
// percent encoding (utf8-pct:), or the value itself.
func decodeHeader(v string) string {
	rest, ok := strings.CutPrefix(v, headerEncoded)
	if !ok {
		return v
	}
	if dec, err := url.PathUnescape(rest); err == nil {
		return dec
	}
	return v
}

// addResult adds one stored call to the evidence: its arguments (what the run
// asked for) and its result, an error result included (what the run was told).
// JSON is read as JSON so an escape is not read as a letter. It reports false
// for a result cut at the size cap, which cannot prove a name absent.
func addResult(set *evidencenames.Evidence, info *nats.ObjectInfo, body []byte) bool {
	if strings.EqualFold(headerValue(info.Headers, headerTruncated), "true") {
		return false
	}
	set.Add(decodeHeader(headerValue(info.Headers, headerArguments)))
	if strings.Contains(strings.ToLower(headerValue(info.Headers, headerType)), "json") && set.AddJSON(body) {
		return true
	}
	set.Add(string(body))
	return true
}

// readStore adds the thread's tool calls of one store to the set and the
// seen keys. It is false when the list or a read failed or a result was cut.
func readStore(set *evidencenames.Evidence, seen map[string]bool, st evidenceStore, namespace, threadID string) bool {
	infos, err := st.List()
	if err != nil && !errors.Is(err, nats.ErrNoObjectsFound) {
		fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: list workspace: %v\n", err)
		return false
	}
	for _, info := range infos {
		if !inThread(info, namespace, threadID) {
			continue
		}
		body, gErr := st.GetBytes(info.Name)
		if gErr != nil || !addResult(set, info, body) {
			fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: tool result %s not usable as evidence: %v\n", info.Name, gErr)
			return false
		}
		seen[relativeKey(info.Name)] = true
	}
	return true
}

// missingCall returns a call the findings name that is not stored, or "".
func missingCall(seen, expected map[string]bool) string {
	for key := range expected {
		if !seen[key] {
			return key
		}
	}
	return ""
}

// readEvidence reads the thread's tool calls from the stores. ok is false when
// any list or read failed, a result was cut, a call the findings name is not
// stored, no call was read, or the token set outgrew its bound: the set then
// does not cover the run and the check must not run.
func readEvidence(stores []evidenceStore, namespace, threadID string,
	expected map[string]bool) (*evidencenames.Evidence, int, bool) {
	set := evidencenames.NewEvidence()
	seen := map[string]bool{}
	for _, st := range stores {
		if !readStore(set, seen, st, namespace, threadID) {
			return nil, 0, false
		}
	}
	if len(seen) == 0 {
		fmt.Println("[fitness-runner] no tool results read for the thread; invented-name check skipped")
		return nil, 0, false
	}
	if key := missingCall(seen, expected); key != "" {
		fmt.Fprintf(os.Stderr, "[fitness-runner] call %s named in the findings is not stored; check skipped\n", key)
		return nil, 0, false
	}
	return set, len(seen), !set.Truncated()
}

// allowedNames are the tokens the answer may repeat without having read them
// from a tool: those of the question, the assertions and the stream's agents.
func allowedNames(outcome *RunOutcome) *evidencenames.Evidence {
	ok := evidencenames.NewEvidence()
	ok.Add(outcome.Question)
	for _, a := range outcome.Assertions {
		ok.Add(a.Raw)
	}
	for _, e := range outcome.Events {
		ok.Add(e.Agent)
		ok.Add(e.GPU)
	}
	return ok
}

// checkInventedNames runs the check for a finished run over the stores. The
// result says whether the check ran and, if so, which names the answer asserts
// that no tool result held. It never fails the run.
func checkInventedNames(stores []evidenceStore, namespace string, outcome *RunOutcome) Evidence {
	synthesis := fitnessscript.FindSynthesis(outcome.Events)
	if namespace == "" || outcome.ThreadID == "" || strings.TrimSpace(synthesis) == "" {
		return Evidence{}
	}
	set, calls, ok := readEvidence(stores, namespace, outcome.ThreadID, expectedCallKeys(outcome.Events))
	if !ok {
		return Evidence{}
	}
	names := evidencenames.Invented(synthesis, set, allowedNames(outcome))
	return Evidence{Captured: true, Calls: calls, InventedNames: names}
}

// bucketMissing is true when err says the bucket was never created, the one
// open error that leaves the other bucket's results a complete record.
func bucketMissing(err error) bool {
	return errors.Is(err, nats.ErrBucketNotFound) || errors.Is(err, nats.ErrStreamNotFound)
}

// captureEvidence opens the workspace buckets and checks the run. NATS is
// reached anonymously, as for the transcript write. Best-effort: a failed
// connect, or any bucket error other than "not found", means the run is not checked.
func captureEvidence(natsURL, namespace string, outcome *RunOutcome) Evidence {
	if natsURL == "" || outcome.ThreadID == "" {
		return Evidence{}
	}
	nc, err := nats.Connect(natsURL, nats.Name("fitness-runner-evidence"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: evidence connect: %v\n", err)
		return Evidence{}
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return Evidence{}
	}
	var stores []evidenceStore
	for _, b := range []string{workspaceBucket, legacyWorkspaceBucket} {
		st, sErr := js.ObjectStore(b)
		switch {
		case sErr == nil:
			stores = append(stores, st)
		case bucketMissing(sErr):
			// This bucket was never created: it holds nothing.
		default:
			fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: open %s: %v\n", b, sErr)
			return Evidence{}
		}
	}
	return checkInventedNames(stores, namespace, outcome)
}
