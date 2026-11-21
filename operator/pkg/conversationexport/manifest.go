/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

// The zip's layout. The names are part of the export's contract.
const (
	FileMarkdown = "conversation.md"
	FileJSON     = "conversation.json"
	FileManifest = "manifest.json"
	DirReceipts  = "receipts"
	DirArtifacts = "artifacts"
	DirMemory    = "memory"
)

// Kinds of the two manifests.
const (
	KindConversation = "kubemoot.conversation-export"
	KindBundle       = "kubemoot.conversation-export-bundle"
)

// What became of one part of the export.
const (
	StatusIncluded    = "included"
	StatusExpired     = "expired"     // Kubemoot held it once and no longer does
	StatusNotKept     = "not-kept"    // this installation does not keep it
	StatusEmpty       = "empty"       // held, and nothing in it belongs to the conversation
	StatusUnavailable = "unavailable" // the store could not be read
)

// Part is one part of a conversation zip and what became of it.
type Part struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	// Count is how many files or facts the part holds.
	Count  int    `json:"count"`
	Reason string `json:"reason,omitempty"`
}

// LeftOut is one entry an export did not include: its key, or an opaque id when the
// key itself holds a credential, and the reason. It never carries a fact's text. It has
// the shape of the memory export's report.
type LeftOut struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// Manifest is the manifest.json of one conversation zip.
type Manifest struct {
	Kind           string    `json:"kind"`
	Origin         string    `json:"origin"`
	ExportedAt     string    `json:"exportedAt"`
	Namespace      string    `json:"namespace"`
	Crew           string    `json:"crew"`
	ConversationID string    `json:"conversationId"`
	TimeZone       string    `json:"timeZone"`
	Turns          int       `json:"turns"`
	Parts          []Part    `json:"parts"`
	LeftOut        []LeftOut `json:"leftOut"`
}

// Entry names a conversation of a bundle and why it is not in it, or the zip that holds it.
type Entry struct {
	ConversationID string `json:"conversationId"`
	// File is the inner zip, for an included conversation.
	File   string `json:"file,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// BundleManifest is the manifest.json of a zip of zips. Every id asked for is in
// exactly one list.
type BundleManifest struct {
	Kind       string  `json:"kind"`
	Origin     string  `json:"origin"`
	ExportedAt string  `json:"exportedAt"`
	Namespace  string  `json:"namespace"`
	Crew       string  `json:"crew"`
	Included   []Entry `json:"included"`
	Refused    []Entry `json:"refused"`
	Expired    []Entry `json:"expired"`
	// Failed lists a conversation whose stores could not be read, so a transient fault is
	// never mistaken for an expired conversation.
	Failed []Entry `json:"failed"`
}
