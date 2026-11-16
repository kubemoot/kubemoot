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

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/evidencenames"
)

// The two buckets the discussion workspace lives in: the named one and the one
// it was stored in before it had its name. WORKSPACE_BUCKET and ARTIFACT_BUCKET
// override them, as they do for the agents.
const (
	workspaceBucket       = "kubemoot_workspace"
	legacyWorkspaceBucket = "kubemoot_discussion_artifacts"
)

// callKeyTail matches the last segment of a tool-call object: {nnn}-{tool}.
var callKeyTail = regexp.MustCompile(`^[0-9]{3,}-`)

// evidenceStore is the slice of a NATS object store the capture reads.
type evidenceStore interface {
	List(opts ...nats.ListObjectsOpt) ([]*nats.ObjectInfo, error)
	GetBytes(name string, opts ...nats.GetObjectOpt) ([]byte, error)
}

// Evidence is what the run's tool results held, kept for the invented-name check
// after the thread's workspace is deleted.
type Evidence struct {
	// Names are the distinct name-shaped tokens of every successful tool result.
	Names []string `json:"evidenceNames,omitempty"`
	// Calls is how many tool results were read.
	Calls int `json:"evidenceCalls,omitempty"`
	// Captured is true when the results were read and the token set is complete,
	// so a name missing from Names is missing from the results.
	Captured bool `json:"evidenceCaptured,omitempty"`
}

// isCallObject is true for a thread's successful tool-call object: its key ends
// in {nnn}-{tool} and its headers do not mark an error.
func isCallObject(info *nats.ObjectInfo, threadID string) bool {
	if info == nil || info.Deleted || !strings.Contains(info.Name, "/"+threadID+"/") {
		return false
	}
	if !callKeyTail.MatchString(info.Name[strings.LastIndex(info.Name, "/")+1:]) {
		return false
	}
	return !strings.EqualFold(info.Headers.Get("kubemoot.error"), "true")
}

// collectEvidence reads every successful tool result of the thread from the
// stores and returns its tokens. It returns Captured=false when the thread has
// no results to read or the set outgrew its bound.
func collectEvidence(stores []evidenceStore, threadID string) Evidence {
	if threadID == "" {
		return Evidence{}
	}
	set := evidencenames.NewEvidence()
	calls := 0
	for _, st := range stores {
		infos, err := st.List()
		if err != nil && !errors.Is(err, nats.ErrNoObjectsFound) {
			fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: list workspace: %v\n", err)
			continue
		}
		for _, info := range infos {
			if !isCallObject(info, threadID) {
				continue
			}
			data, gErr := st.GetBytes(info.Name)
			if gErr != nil {
				fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: read %s: %v\n", info.Name, gErr)
				return Evidence{}
			}
			set.Add(string(data))
			calls++
		}
	}
	if calls == 0 || set.Truncated() {
		return Evidence{}
	}
	return Evidence{Names: set.Tokens(), Calls: calls, Captured: true}
}

// captureEvidence opens the workspace buckets and collects the thread's
// evidence. Best-effort: any failure yields an empty Evidence and the check is
// skipped for the run.
func captureEvidence(natsURL, threadID string, getenv func(string) string) Evidence {
	if natsURL == "" || threadID == "" {
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
	for _, b := range workspaceBuckets(getenv) {
		if st, sErr := js.ObjectStore(b); sErr == nil {
			stores = append(stores, st)
		}
	}
	return collectEvidence(stores, threadID)
}

// workspaceBuckets are the buckets to read, the named one first, each once.
func workspaceBuckets(getenv func(string) string) []string {
	current, legacy := workspaceBucket, legacyWorkspaceBucket
	if v := getenv("WORKSPACE_BUCKET"); v != "" {
		current = v
	}
	if v := getenv("ARTIFACT_BUCKET"); v != "" {
		legacy = v
	}
	if current == legacy {
		return []string{current}
	}
	return []string{current, legacy}
}
