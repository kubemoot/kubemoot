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
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/fitness-runner/internal/evidencenames"
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
	headerOperation  = "gen_ai.operation.name"
	operationTool    = "execute_tool"
	headerArguments  = "gen_ai.tool.call.arguments"
	headerTruncated  = "kubemoot.truncated"
	headerType       = "Content-Type"
	eventThreadFound = "thread_found"
	headerEncoded    = "utf8-pct:"
)

var (
	// callKeyTail matches the last segment of a tool-call object: {nnn}-{tool}.
	callKeyTail = regexp.MustCompile(`^[0-9]{3,}-`)
	// callLine matches the key of a [CALL key=...] line of a finding, relative to the thread.
	callLine = regexp.MustCompile(`\[CALL key=([^\s\]]+)`)
	// unstoredCall matches the line of a call by an agent that stores no results, which has no
	// key, with the result block posted right after it (group 1 for a result or an error text).
	// Such a call is not in the store; the findings hold all there is of it.
	unstoredCall = regexp.MustCompile(`(?s)\[CALL tool=[^\]]*\]` +
		`(?:\n\[(?:FAILED )?RESULT\]\n(.*?)\n\[END RESULT\]|\n\[EMPTY RESULT\]\n[^\n]*\n\[END RESULT\])?`)
	// notStoredLine matches a call whose result is in neither the store nor the findings.
	notStoredLine = regexp.MustCompile(`\[NOT STORED tool=`)
)

// Evidence is the outcome of the invented-name check, stored in the transcript.
type Evidence struct {
	// Captured is true when every tool result of the thread was read, so a name
	// missing from them is missing from the run's evidence. When false the check
	// did not run and InventedNames is empty.
	Captured bool `json:"evidenceCaptured,omitempty"`
	// Calls is how many tool results were read.
	Calls int `json:"evidenceCalls,omitempty"`
	// StragglerTimeout is true when the run waited the full wait for a call the
	// findings name and it never landed; the check did not run. Counting it over a
	// suite's transcripts shows a systematic gap.
	StragglerTimeout bool `json:"evidenceStragglerTimeout,omitempty"`
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

// answeringEvents are the events of the answering thread: those from the last
// thread_found on. The gateway announces a second thread when the coordinator
// restarted and took the request again, and the earlier thread's findings
// belong to calls of a thread that is not read. Only thread_found carries the
// thread id, so the split is by position in the stream.
func answeringEvents(events []SignalEvent) []SignalEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventThreadFound {
			return events[i:]
		}
	}
	return events
}

// expectedCallKeys are the keys of the [CALL key=...] lines of the answering
// thread's findings: the calls the run says it made.
func expectedCallKeys(events []SignalEvent) map[string]bool {
	out := map[string]bool{}
	for _, e := range answeringEvents(events) {
		for _, m := range callLine.FindAllStringSubmatch(e.Content, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// inlineEvidence is what the findings of the answering thread hold of calls that are not in the
// store: the calls of agents that store no results, with the results posted in the findings.
type inlineEvidence struct {
	// calls is how many such calls the findings list.
	calls int
	// texts are their call lines and posted results.
	texts []string
	// incomplete is true when a call's result is in neither the store nor the findings, so no
	// name can be shown absent from the run's evidence.
	incomplete bool
}

// inlineEvidenceOf reads the calls of the answering thread's findings that are not stored. An agent
// that stores nothing posts what it saw in the finding, so a name in it is evidence the run read.
func inlineEvidenceOf(events []SignalEvent) inlineEvidence {
	var out inlineEvidence
	for _, e := range answeringEvents(events) {
		if notStoredLine.MatchString(e.Content) {
			out.incomplete = true
		}
		for _, m := range unstoredCall.FindAllStringSubmatch(e.Content, -1) {
			out.calls++
			out.texts = append(out.texts, m[0])
			if m[1] != "" {
				out.texts = append(out.texts, m[1])
			}
		}
	}
	return out
}

// decodeHeader returns the text a header value stands for. The agent runtime
// writes a value that is not printable ASCII as "utf8-pct:" and the UTF-8 bytes
// with each byte outside printable ASCII, and every percent sign, as %XX. A value
// that is not validly encoded is returned as it is. It follows
// artifact-access/internal/store DecodeHeaderValue; both run
// test/fixtures/header-values.json.
func decodeHeader(v string) string {
	rest, ok := strings.CutPrefix(v, headerEncoded)
	if !ok {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c < 0x20 || c >= 0x7F {
			return v
		}
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(rest) || hexDigit(rest[i+1]) < 0 || hexDigit(rest[i+2]) < 0 {
			return v
		}
		b.WriteByte(byte(hexDigit(rest[i+1])<<4 | hexDigit(rest[i+2])))
		i += 2
	}
	if !utf8.ValidString(b.String()) {
		return v
	}
	return b.String()
}

// hexDigit is the value of an ASCII hex digit, or -1.
func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
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

// stragglerWait bounds the wait for a call the findings name but the store does
// not list yet. The wait ends by state, when every named call is stored; the
// deadline is only a safety net for a call that never lands.
type stragglerWait struct {
	deadline time.Duration
	interval time.Duration
}

var defaultWait = stragglerWait{deadline: 30 * time.Second, interval: 2 * time.Second}

// readOnce lists and reads the thread's calls once. ok is false when a list or
// read failed or a result was cut.
func readOnce(stores []evidenceStore, namespace, threadID string) (*evidencenames.Evidence, map[string]bool, bool) {
	set := evidencenames.NewEvidence()
	seen := map[string]bool{}
	for _, st := range stores {
		if !readStore(set, seen, st, namespace, threadID) {
			return nil, nil, false
		}
	}
	return set, seen, true
}

// readOutcome is how a read of the thread's calls ended.
type readOutcome int

const (
	readComplete readOutcome = iota // every call read; the set covers the run
	readSkipped                     // a failure, a cut result, or no call: not checked
	readTimedOut                    // a named call never landed within the wait
)

// readEvidence reads the thread's tool calls from the stores, with the calls the findings hold that are
// not stored (inline): their lines and results join the set and count as read calls. When the findings
// name a call that is not stored yet it lists again until all are, or the wait
// deadline passes. ok is false when a list or read failed, a result was cut, a
// named call never landed, no call was read, or the token set outgrew its bound:
// the set then does not cover the run and the check must not run.
func readEvidence(stores []evidenceStore, namespace, threadID string, expected map[string]bool,
	inline inlineEvidence, wait stragglerWait) (*evidencenames.Evidence, int, readOutcome) {
	deadline := time.Now().Add(wait.deadline)
	for {
		set, seen, ok := readOnce(stores, namespace, threadID)
		if !ok {
			return nil, 0, readSkipped
		}
		key := missingCall(seen, expected)
		if key == "" {
			if len(seen)+inline.calls == 0 {
				fmt.Println("[fitness-runner] no usable tool results for the thread; invented-name check skipped")
				return nil, 0, readSkipped
			}
			for _, text := range inline.texts {
				set.Add(text)
			}
			if set.Truncated() {
				fmt.Println("[fitness-runner] the evidence outgrew its bound; invented-name check skipped")
				return nil, 0, readSkipped
			}
			return set, len(seen) + inline.calls, readComplete
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "[fitness-runner] call %s named in the findings is not stored after waiting %s; "+
				"check skipped (evidence straggler timeout)\n", key, wait.deadline)
			return nil, 0, readTimedOut
		}
		time.Sleep(wait.interval)
	}
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
func checkInventedNames(stores []evidenceStore, namespace string, outcome *RunOutcome, wait stragglerWait) Evidence {
	synthesis := fitnessscript.FindSynthesis(outcome.Events)
	if namespace == "" || outcome.ThreadID == "" || strings.TrimSpace(synthesis) == "" {
		return Evidence{}
	}
	inline := inlineEvidenceOf(outcome.Events)
	if inline.incomplete {
		fmt.Println("[fitness-runner] a call's result is neither stored nor in the findings; invented-name check skipped")
		return Evidence{}
	}
	set, calls, res := readEvidence(stores, namespace, outcome.ThreadID, expectedCallKeys(outcome.Events), inline, wait)
	if res != readComplete {
		return Evidence{StragglerTimeout: res == readTimedOut}
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
	return checkInventedNames(stores, namespace, outcome, defaultWait)
}
