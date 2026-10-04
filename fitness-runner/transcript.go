/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
)

// transcriptDefaultTTL is the bucket max-age used only when this runner is the
// first writer to create the bucket. In practice the operator's XLSX path
// usually creates kubemoot_fitness_artifacts first; EnsureObjectStore then
// returns the existing bucket and this TTL is ignored (Object Store TTL is
// bucket-level, shared by transcripts and XLSX artifacts alike).
const transcriptDefaultTTL = 720 * time.Hour

// maybeWriteTranscript writes the per-iteration transcript JSON to NATS Object
// Store when the operator wired the suite-iteration env (TRANSCRIPT_KEY +
// NATS_URL). It is best-effort: a failure logs and never fails the fitness run
// (the assertion result is the source of truth). Standalone CrewFitness tests (no
// suite context) have no TRANSCRIPT_KEY and are skipped.
func maybeWriteTranscript(outcome *RunOutcome, getenv func(string) string) {
	key := getenv("TRANSCRIPT_KEY")
	if key == "" {
		return // not a suite iteration - nothing to capture
	}
	natsURL := getenv("NATS_URL")
	if natsURL == "" {
		fmt.Fprintln(os.Stderr, "[fitness-runner] TRANSCRIPT_KEY set but NATS_URL empty — skipping transcript")
		return
	}
	bucket := getenv("TRANSCRIPT_BUCKET")
	if bucket == "" {
		bucket = "kubemoot_fitness_artifacts"
	}

	blob, err := json.Marshal(outcome)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: marshal transcript: %v\n", err)
		return
	}
	if err := putObject(natsURL, bucket, key, blob, transcriptDefaultTTL); err != nil {
		fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: write transcript %s: %v\n", key, err)
		return
	}
	fmt.Printf("[fitness-runner] wrote transcript %s (%d events, %d bytes)\n",
		key, len(outcome.Events), len(blob))
}

// putObject connects to NATS, ensures the Object Store bucket exists (matching
// the operator's EnsureObjectStore semantics — bucket-level max-age TTL), and
// writes the blob at key. Each call opens and drains its own connection: the
// runner is a short-lived Job that writes exactly one transcript.
func putObject(natsURL, bucket, key string, data []byte, ttl time.Duration) error {
	nc, err := nats.Connect(natsURL, nats.Name("fitness-runner-transcript"))
	if err != nil {
		return fmt.Errorf("connect NATS: %w", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}

	store, err := js.ObjectStore(bucket)
	if err != nil {
		// Bucket doesn't exist yet — create it with the TTL (first writer).
		store, err = js.CreateObjectStore(&nats.ObjectStoreConfig{
			Bucket:      bucket,
			Description: "Kubemoot fitness artifacts (XLSX + per-iteration transcripts)",
			TTL:         ttl,
		})
		if err != nil {
			return fmt.Errorf("ensure object store %q: %w", bucket, err)
		}
	}

	if _, err := store.PutBytes(key, data); err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	return nil
}
