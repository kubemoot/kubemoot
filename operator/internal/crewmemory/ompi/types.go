/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package ompi exports a crew's memory in the interchange representation of
// the Open Memory Protocol Initiative (OMPI) exchange-and-runtime working spec
// v0.2 (editors' draft of 2026-10-14, OMPI pull request #13): one memory object
// per crew fact and a manifest, with an optional Markdown projection of the
// runtime representation.
//
// The spec is a draft and has no official validator. The JSON Schemas in
// schema/ are Kubemoot's own, derived from the field table in section 3.2 and
// the manifest in section 8.1; see schema/README.md.
package ompi

// SpecVersion is the protocol_version the export declares.
const SpecVersion = "0.2"

// SpecCommit pins the spec text this package follows: the head of OMPI pull
// request #13 in The-AI-Disclosures-Project/Open-Memory-Protocol, file
// working-group/exchange-and-runtime/2026-10-14-working-spec-v0.2.md.
const SpecCommit = "20235eaa9f3cb27cdce9e86667aa56ac963abeae"

// ExtensionKey names the field that holds Kubemoot-only data on a memory and
// on the manifest. The spec lets an implementation add fields (section 3.2).
const ExtensionKey = "x-kubemoot"

// Version is the spec's version slot: an opaque value and its declared comparator.
type Version struct {
	Value      uint64 `json:"value"`
	Comparator string `json:"comparator"`
}

// Contributor is one provenance entry (section 4.1).
type Contributor struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Contribution string `json:"contribution,omitempty"`
	At           string `json:"at,omitempty"`
}

// Memory is the interchange form of one memory object (section 3.2). Fields
// the crew memory cannot fill are left out, never invented.
type Memory struct {
	Identifier string        `json:"identifier"`
	Path       string        `json:"path,omitempty"`
	Title      string        `json:"title,omitempty"`
	MediaType  string        `json:"media_type"`
	Scope      []string      `json:"scope,omitempty"`
	CreatedAt  string        `json:"created_at"`
	UpdatedAt  string        `json:"updated_at,omitempty"`
	Version    *Version      `json:"version,omitempty"`
	Provenance []Contributor `json:"provenance,omitempty"`
	Body       string        `json:"body"`
	Extension  MemoryExt     `json:"x-kubemoot"`
}

// MemoryExt is the Kubemoot-only data on a memory.
type MemoryExt struct {
	Namespace string `json:"namespace"`
	Crew      string `json:"crew"`
	Topic     string `json:"topic"`
	Key       string `json:"key"`
	// UsedAt is when the fact was last read; it drives eviction and expiry.
	UsedAt string `json:"usedAt,omitempty"`
	// EvidenceUnavailable says the fact rests on something the store does not
	// record (section 4.4): the memory carries no evidence because none exists.
	EvidenceUnavailable bool `json:"evidenceUnavailable"`
}

// Manifest is the export's manifest (section 8.1).
type Manifest struct {
	ProtocolVersion string      `json:"protocol_version"`
	Profiles        []string    `json:"profiles"`
	Origin          string      `json:"origin"`
	ExportedAt      string      `json:"exported_at"`
	Selection       string      `json:"selection"`
	Intent          string      `json:"intent"`
	MediaTypes      []string    `json:"media_types"`
	Extension       ManifestExt `json:"x-kubemoot"`
}

// ManifestExt is the Kubemoot-only data on the manifest.
type ManifestExt struct {
	Bucket    string `json:"bucket"`
	Namespace string `json:"namespace"`
	Crew      string `json:"crew"`
	// Skipped counts entries that were read but left out, with no usable time
	// or unreadable; it is non-zero exactly when Selection is "selection".
	Skipped int `json:"skipped"`
}

// Export is a manifest and its memories.
type Export struct {
	Manifest Manifest
	Memories []Memory
	// Skipped lists the entries that were left out and why.
	Skipped []Omitted
}

// Omitted names a crew-memory entry the export left out.
type Omitted struct {
	Key    string
	Reason string
}
