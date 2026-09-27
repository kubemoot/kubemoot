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
	"fmt"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	"github.com/javajon/kubemoot/operator/internal/crewscope"
)

// Bucket is the NATS KV bucket holding crew working memory.
const Bucket = "kubemoot_crew_memory"

// KV is the subset of the NATS publisher the migration uses.
type KV interface {
	ListKVKeys(bucket string) ([]string, error)
	GetKVValue(bucket, key string) ([]byte, error)
	PutKVValue(bucket, key string, value []byte) error
	DeleteKVKey(bucket, key string) error
}

// Migrator renames unscoped crew-memory keys once at operator startup. A key is
// renamed when exactly one namespace has a Crew of that name; keys whose crew
// exists in several namespaces (ambiguous) or in none (orphaned) are left in
// place and logged.
type Migrator struct {
	Reader client.Reader
	KV     KV
}

// NeedLeaderElection runs the migration only on the elected leader.
func (m *Migrator) NeedLeaderElection() bool { return true }

// Start runs the migration once and returns. A failure is logged, never fatal:
// the unscoped keys stay readable to nobody but are not lost.
func (m *Migrator) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("crew-memory-migration")
	if err := m.Run(ctx, log); err != nil {
		log.Error(err, "Crew memory key migration failed; unscoped keys left in place")
	}
	return nil
}

// Run lists the Crews and the bucket's keys, then renames every unscoped key
// that maps to exactly one namespace.
func (m *Migrator) Run(ctx context.Context, log logr.Logger) error {
	if m.KV == nil || m.Reader == nil {
		return nil
	}
	crews := &kubemootv1alpha1.CrewList{}
	if err := m.Reader.List(ctx, crews); err != nil {
		return fmt.Errorf("list crews: %w", err)
	}
	keys, err := m.KV.ListKVKeys(Bucket)
	if err != nil {
		return fmt.Errorf("list %s keys: %w", Bucket, err)
	}
	p := Plan(keys, NewIndex(crews.Items))
	logLeftUnscoped(log, p)
	renamed := m.applyRenames(log, p.Renames)
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

// applyRenames performs the renames and returns how many succeeded.
func (m *Migrator) applyRenames(log logr.Logger, renames []Rename) int {
	renamed := 0
	for _, r := range renames {
		if m.rename(log, r) {
			renamed++
		}
	}
	return renamed
}

// rename copies one value to its scoped key and deletes the unscoped key. An
// existing scoped key wins; the unscoped copy is then only deleted.
func (m *Migrator) rename(log logr.Logger, r Rename) bool {
	value, err := m.KV.GetKVValue(Bucket, r.From)
	if err != nil {
		log.V(1).Info("Crew memory key not read", "key", r.From, "error", err.Error())
		return false
	}
	if value == nil {
		return false
	}
	if existing, _ := m.KV.GetKVValue(Bucket, r.To); existing == nil {
		if err := m.KV.PutKVValue(Bucket, r.To, value); err != nil {
			log.Info("Crew memory key not renamed", "from", r.From, "to", r.To, "error", err.Error())
			return false
		}
	}
	if err := m.KV.DeleteKVKey(Bucket, r.From); err != nil {
		log.Info("Crew memory key copied but the unscoped key was not deleted", "key", r.From, "error", err.Error())
	}
	return true
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
