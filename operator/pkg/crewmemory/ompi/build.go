/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/secretscreen"
)

const (
	plainText  = "text/plain"
	comparator = "integer"

	// SelectionSnapshot and SelectionSelection are the manifest's account of
	// completeness: every readable fact, or fewer because some were left out.
	SelectionSnapshot  = "snapshot"
	SelectionSelection = "selection"
)

// idNamespace seeds the UUIDv5 identifiers. It is fixed, so a fact keeps its
// identifier across exports.
var idNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://kubemoot.org/ompi/crew-memory"))

// Options are the parts of an export that do not come from the facts.
type Options struct {
	// ExportedAt is the time the export is as of (manifest exported_at).
	ExportedAt time.Time
	// Origin is the store identifier for the manifest. When empty, a stable
	// one is derived from the bucket, namespace and crew.
	Origin string
	// AllowUnscreened exports facts that hold a credential. By default such a
	// fact is left out and listed in Skipped, because the spec (section 3.5.6)
	// says credentials must not be exported as memory.
	AllowUnscreened bool
}

// ScopeTag names the crew that owns a memory: crew:<namespace>/<crew>. The
// crew is one owner however many agents it has; the spec treats scope tags as
// opaque strings, and a receiver reads this one as the owner's name.
func ScopeTag(s crewscope.Scope) string {
	return "crew:" + s.Namespace + "/" + s.Crew
}

// Identifier is the stable identifier of a fact: a UUIDv5 of
// <namespace>.<crew>.<topic>.<key>, the fact's key in the store.
func Identifier(f crewmemory.Fact) string {
	return uuid.NewSHA1(idNamespace, []byte(f.StoreKey())).String()
}

// DefaultOrigin derives a stable origin for one crew's memory store. Pass an
// explicit origin to Options when several clusters hold a crew of the same name.
func DefaultOrigin(s crewscope.Scope) string {
	name := crewmemory.Bucket + "/" + s.Namespace + "/" + s.Crew
	return "store:" + uuid.NewSHA1(idNamespace, []byte(name)).String()
}

// Build maps a crew's facts to memory objects and a manifest. Facts may come
// in any order; the result is deterministic. A fact with no time the system
// recorded it cannot meet the required created_at, and a fact whose text holds
// a credential (unless opts.AllowUnscreened), are listed in Skipped.
func Build(scope crewscope.Scope, res crewmemory.ReadResult, opts Options) (Export, error) {
	exp := Export{Memories: []Memory{}}
	for _, s := range res.Skipped {
		exp.Skipped = append(exp.Skipped, unreadable(scope, s, opts))
	}
	seen := map[string]string{}
	for _, p := range place(res.Facts) {
		if o, held := credentialOmission(p.fact, opts); held {
			exp.Skipped = append(exp.Skipped, o)
			continue
		}
		m, ok := memoryOf(p)
		if !ok {
			exp.Skipped = append(exp.Skipped, Omitted{Key: p.fact.StoreKey(), Reason: "no time the fact was recorded"})
			continue
		}
		if other, dup := seen[m.Identifier]; dup {
			return Export{}, fmt.Errorf("identifier %s given to both %q and %q", m.Identifier, other, p.fact.StoreKey())
		}
		seen[m.Identifier] = p.fact.StoreKey()
		exp.Memories = append(exp.Memories, m)
	}
	exp.Manifest = manifestOf(scope, exp, opts)
	return exp, nil
}

// memoryOf maps one fact. created_at is the agent's learnedAt: the store does
// not keep the time of the first write, and the store's own time (Recorded) is
// that of the latest write, so learnedAt is the closest record of when the crew
// learned the fact. Recorded stands in only when learnedAt is missing.
func memoryOf(p placed) (Memory, bool) {
	f := p.fact
	created := f.LearnedAt
	if created.IsZero() {
		created = f.Recorded
	}
	if created.IsZero() {
		return Memory{}, false
	}
	return Memory{
		Identifier:      Identifier(f),
		ProtocolVersion: SpecVersion,
		Path:            p.path,
		Title:           f.Topic + " / " + f.Key,
		MediaType:       plainText,
		Scope:           []string{ScopeTag(f.Scope)},
		CreatedAt:       formatTime(created),
		UpdatedAt:       formatTime(f.Recorded),
		Version:         versionOf(f),
		Provenance:      provenanceOf(f),
		Body:            f.Value,
		Extension: MemoryExt{
			Namespace: f.Scope.Namespace, Crew: f.Scope.Crew, Topic: f.Topic, Key: f.Key,
			UsedAt: formatTime(f.UsedAt), LearnedInThread: f.LearnedInThread, EvidenceUnavailable: true,
		},
	}, true
}

func versionOf(f crewmemory.Fact) *Version {
	if f.Revision == 0 {
		return nil
	}
	return &Version{Value: f.Revision, Comparator: comparator}
}

// provenanceOf names the writing agent. With no recorded writer the field is
// left out, so a receiver reads the author as unknown (section 3.1.5).
func provenanceOf(f crewmemory.Fact) []Contributor {
	if f.LearnedBy == "" {
		return nil
	}
	return []Contributor{{ID: f.LearnedBy, Kind: "agent", Contribution: "authored", At: formatTime(f.LearnedAt)}}
}

func manifestOf(scope crewscope.Scope, exp Export, opts Options) Manifest {
	origin := opts.Origin
	if origin == "" {
		origin = DefaultOrigin(scope)
	}
	selection := SelectionSnapshot
	if len(exp.Skipped) > 0 {
		selection = SelectionSelection
	}
	return Manifest{
		ProtocolVersion: SpecVersion,
		Profiles:        []string{"minimal"},
		Origin:          origin,
		ExportedAt:      formatTime(opts.ExportedAt),
		Selection:       selection,
		Intent:          "copy",
		MediaTypes:      []string{plainText},
		Extension: ManifestExt{
			Bucket: crewmemory.Bucket, Namespace: scope.Namespace, Crew: scope.Crew, Skipped: len(exp.Skipped),
		},
	}
}

// formatTime renders an ISO 8601 UTC timestamp, or "" for a zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// credentialOmission says why a fact is left out when its store key, topic, key
// or value holds a credential. The key is checked first with the strict
// redaction, so a credential in it is never printed: the entry is named by an
// opaque id instead, and a false match there costs only that id. The value is
// checked with the tighter screen, and the report names its shape, never its text.
func credentialOmission(f crewmemory.Fact, opts Options) (Omitted, bool) {
	if opts.AllowUnscreened {
		return Omitted{}, false
	}
	if keyHoldsCredential(f) {
		return Omitted{Key: opaqueKey(f), Reason: "its topic or key holds a credential; not exported as memory"}, true
	}
	if shape, found := secretscreen.Find(f.Value); found {
		reason := "its value holds a credential (" + shape + "); not exported as memory"
		return Omitted{Key: f.StoreKey(), Reason: reason}, true
	}
	return Omitted{}, false
}

// keyHoldsCredential is true when the strict redaction would change the topic,
// the key or the whole store key.
func keyHoldsCredential(f crewmemory.Fact) bool {
	for _, text := range []string{f.Topic, f.Key, f.StoreKey()} {
		if secretscreen.Redact(text) != text {
			return true
		}
	}
	return false
}

// opaqueKey names a fact whose store key holds a credential: <namespace>.<crew>.#<id>,
// where the id is the start of the one-way hash that also seeds the fact's identifier.
func opaqueKey(f crewmemory.Fact) string {
	return opaqueName(f.Scope, Identifier(f))
}

func opaqueName(s crewscope.Scope, hash string) string {
	const idLen = 8
	return s.Namespace + "." + s.Crew + ".#" + hash[:idLen]
}

// unreadable reports an entry the reader could not turn into a fact. Its key
// and reason are printed only when strict redaction leaves them unchanged.
func unreadable(scope crewscope.Scope, s crewmemory.Skipped, opts Options) Omitted {
	if opts.AllowUnscreened || secretscreen.Redact(s.Key) == s.Key {
		reason := s.Reason
		if !opts.AllowUnscreened {
			reason = secretscreen.Redact(reason)
		}
		return Omitted{Key: s.Key, Reason: reason}
	}
	hash := uuid.NewSHA1(idNamespace, []byte(s.Key)).String()
	return Omitted{Key: opaqueName(scope, hash), Reason: secretscreen.Redact(s.Reason)}
}
