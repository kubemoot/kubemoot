/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package crewmemory holds the one-time startup migration of crew working-memory
// keys in the kubemoot_crew_memory bucket from the unscoped <crew>.<topic>.<key>
// to <ns>.<crew>.<topic>.<key>.
package crewmemory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	memory "github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// KV is the subset of the NATS publisher the migration uses.
type KV interface {
	// CrewMemoryKeys lists the bucket's keys and reports a bus that cannot be reached as
	// an error (bus.ErrNotReady), never as an empty list.
	CrewMemoryKeys() ([]string, error)
	// CrewMemoryGet reads one key: not found is (nil, false, nil), an unreachable bus is
	// bus.ErrNotReady.
	CrewMemoryGet(key string) ([]byte, bool, error)
	CrewMemoryPut(key string, value []byte) error
	CrewMemoryDelete(key string) error
}

// Migrator renames unscoped crew-memory keys once at operator startup. A key is
// renamed when exactly one namespace has a Crew of that name; keys whose crew
// exists in several namespaces (ambiguous) or in none (orphaned) are left in
// place and logged.
type Migrator struct {
	Reader client.Reader
	KV     KV
	// Backoff paces the retries; the zero value is kubemootnats.DefaultBackoff.
	Backoff kubemootnats.Backoff
}

// NeedLeaderElection runs the migration only on the elected leader.
func (m *Migrator) NeedLeaderElection() bool { return true }

// Start runs the migration and returns once it has run, or when ctx ends. While NATS is
// down or the Crew list is not readable it retries with backoff, so the unscoped keys are
// migrated once the bus is back instead of staying unreadable until the next operator
// restart.
func (m *Migrator) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("crew-memory-migration")
	_ = kubemootnats.RetryUntilNoError(ctx, func() error {
		return m.Run(ctx, log)
	}, m.Backoff, func(err error, retryIn time.Duration) {
		// A bus that is not ready is expected after an outage; any other failure is not.
		if errors.Is(err, bus.ErrNotReady) {
			log.Info("Crew memory key migration waits for the bus", "err", err.Error(), "retryIn", retryIn.String())
			return
		}
		log.Error(err, "Crew memory key migration failed; unscoped keys left in place until it succeeds", "retryIn", retryIn.String())
	})
	return nil
}

// Run lists the Crews and the bucket's keys, then renames every unscoped key
// that maps to exactly one namespace. A key list that cannot be read is an error: nothing
// is planned from a bus that cannot be reached.
func (m *Migrator) Run(ctx context.Context, log logr.Logger) error {
	if m.KV == nil || m.Reader == nil {
		return nil
	}
	crews := &kubemootv1alpha1.CrewList{}
	if err := m.Reader.List(ctx, crews); err != nil {
		return fmt.Errorf("list crews: %w", err)
	}
	keys, err := m.KV.CrewMemoryKeys()
	if err != nil {
		return fmt.Errorf("list %s keys: %w", memory.Bucket, err)
	}
	p := Plan(keys, NewIndex(crews.Items))
	logLeftUnscoped(log, p)
	renamed, err := m.applyRenames(log, p.Renames)
	if err != nil {
		return err
	}
	if renamed > 0 || len(p.Ambiguous) > 0 || len(p.Orphaned) > 0 {
		log.Info("Crew memory key migration done", "renamed", renamed,
			"ambiguous", len(p.Ambiguous), "orphaned", len(p.Orphaned))
	}
	return nil
}

// logLeftUnscoped logs every key the plan leaves in place.
func logLeftUnscoped(log logr.Logger, p MigrationPlan) {
	for _, key := range p.Ambiguous {
		log.Info("Crew memory key left unscoped: its crew name exists in several namespaces", "key", key)
	}
	for _, key := range p.Orphaned {
		log.Info("Crew memory key left unscoped: no Crew of that name exists", "key", key)
	}
}

// applyRenames performs the renames and returns how many succeeded. It stops at the first
// bus that cannot be reached and returns that error, so the migration runs again once the
// bus is back; the keys already renamed are gone from the next key list.
func (m *Migrator) applyRenames(log logr.Logger, renames []Rename) (int, error) {
	renamed := 0
	for _, r := range renames {
		ok, err := m.rename(log, r)
		if err != nil {
			return renamed, err
		}
		if ok {
			renamed++
		}
	}
	return renamed, nil
}

// rename copies one value to its scoped key and deletes the unscoped key. An
// existing scoped key wins; the unscoped copy is then only deleted. It returns an error
// only for a bus that cannot be reached; any other failure leaves the unscoped key in
// place and is logged.
func (m *Migrator) rename(log logr.Logger, r Rename) (bool, error) {
	value, found, err := m.KV.CrewMemoryGet(r.From)
	if err != nil {
		return false, m.notReadyOrLog(log, "Crew memory key not read", r, err)
	}
	if !found {
		return false, nil
	}
	_, scoped, err := m.KV.CrewMemoryGet(r.To)
	if err != nil {
		return false, m.notReadyOrLog(log, "Crew memory scoped key not read", r, err)
	}
	if !scoped {
		if err := m.KV.CrewMemoryPut(r.To, value); err != nil {
			return false, m.notReadyOrLog(log, "Crew memory key not renamed", r, err)
		}
	}
	if err := m.KV.CrewMemoryDelete(r.From); err != nil {
		return false, m.notReadyOrLog(log, "Crew memory key copied but the unscoped key was not deleted", r, err)
	}
	return true, nil
}

// notReadyOrLog returns err when the bus is not ready, and otherwise logs it and returns nil.
func (m *Migrator) notReadyOrLog(log logr.Logger, msg string, r Rename, err error) error {
	if errors.Is(err, bus.ErrNotReady) {
		return fmt.Errorf("%s (%s): %w", msg, r.From, err)
	}
	log.Info(msg, "from", r.From, "to", r.To, "error", err.Error())
	return nil
}

// Index maps each Crew name to the namespaces that hold a Crew of that name.
type Index struct {
	namespaces map[string][]string
	scopes     map[crewscope.Scope]bool
}

// NewIndex builds the index from the cluster's Crews.
func NewIndex(crews []kubemootv1alpha1.Crew) Index {
	idx := Index{namespaces: map[string][]string{}, scopes: map[crewscope.Scope]bool{}}
	for i := range crews {
		c := &crews[i]
		idx.namespaces[c.Name] = append(idx.namespaces[c.Name], c.Namespace)
		idx.scopes[crewscope.Scope{Namespace: c.Namespace, Crew: c.Name}] = true
	}
	return idx
}

// Rename moves one key.
type Rename struct {
	From string
	To   string
}

// MigrationPlan is what the migration does with each key.
type MigrationPlan struct {
	Renames   []Rename
	Ambiguous []string
	Orphaned  []string
}

// Plan classifies each key: already scoped (skipped), renamed, ambiguous, or
// orphaned. A key that reads both as scoped (<ns>.<crew>. of an existing Crew)
// and as unscoped (<crew>. of an existing Crew of another name) is ambiguous.
func Plan(keys []string, idx Index) MigrationPlan {
	var p MigrationPlan
	for _, key := range keys {
		crew, rest, ok := crewscope.SplitLegacyMemoryKey(key)
		switch {
		case idx.isScoped(key) && idx.readsAsOtherLegacyCrew(key):
			p.Ambiguous = append(p.Ambiguous, key)
			continue
		case idx.isScoped(key):
			continue
		case !ok:
			p.Orphaned = append(p.Orphaned, key)
			continue
		}
		switch nss := idx.namespaces[crew]; len(nss) {
		case 0:
			p.Orphaned = append(p.Orphaned, key)
		case 1:
			to := crewscope.MemoryKey{Scope: crewscope.Scope{Namespace: nss[0], Crew: crew}, Rest: rest}.Key()
			p.Renames = append(p.Renames, Rename{From: key, To: to})
		default:
			p.Ambiguous = append(p.Ambiguous, key)
		}
	}
	return p
}

// readsAsOtherLegacyCrew reports whether the scoped key's namespace token is
// also the name of an existing Crew other than the key's own crew, so the key
// could equally be an unscoped fact of that Crew. A crew named like its own
// namespace (<ns>.<ns>.) is the common case and reads as scoped.
func (idx Index) readsAsOtherLegacyCrew(key string) bool {
	k, err := crewscope.ParseMemoryKey(key)
	return err == nil && k.Namespace != k.Crew && len(idx.namespaces[k.Namespace]) > 0
}

// isScoped reports whether key already starts with the namespace and name of an
// existing Crew.
func (idx Index) isScoped(key string) bool {
	k, err := crewscope.ParseMemoryKey(key)
	return err == nil && idx.scopes[k.Scope]
}
