/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/askerzone"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/ompi"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// ErrInvalidID means a conversation id is not 1 to 128 letters, digits, '-' or '_'.
var ErrInvalidID = errors.New(crewscope.ErrInvalidConversationID)

// MaxBundle is how many conversations one request may ask for.
const MaxBundle = 50

// Allow is the one place that decides whether the caller may export a conversation.
// It returns ErrRefused to refuse. A Builder with no Allow refuses every conversation.
// Until a conversation has an owner, the door that
// builds the Builder decides with the access it already checked for the crew; identity
// phase 1 replaces this function with a check of the conversation's owner.
type Allow func(ctx context.Context, scope crewscope.Scope, id string) error

// Builder assembles conversation zips.
type Builder struct {
	// Source reads the conversation.
	Source Source
	// Memory opens a read-only reader of the crew's memory. Nil means this
	// installation offers no memory part; the manifest says so.
	Memory func() (crewmemory.EntryReader, error)
	// Allow decides who may export; nil refuses every conversation.
	Allow Allow
	// Now is the export's time; nil means time.Now.
	Now func() time.Time
}

// Options are the caller's choices for one export.
type Options struct {
	// TimeZone is the asker's IANA zone. Times are written in it; one that is not in the
	// IANA database, or none, writes them in UTC.
	TimeZone string
}

// prepared is a conversation checked and read, ready to be written.
type prepared struct {
	scope  crewscope.Scope
	id     string
	held   Held
	zone   string
	loc    *time.Location
	now    time.Time
	memory memoryPart
}

func (b *Builder) clock() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func zoneOf(name string) (string, *time.Location) {
	if zone, ok := askerzone.Valid(name); ok {
		if loc, err := time.LoadLocation(zone); err == nil {
			return zone, loc
		}
	}
	return "UTC", time.UTC
}

// prepare checks the id and the caller's right to it, then reads what is held. It
// writes nothing, so a refusal or an expired conversation is known before a byte is sent.
func (b *Builder) prepare(
	ctx context.Context, scope crewscope.Scope, id string, opts Options, mem *memoryRead,
) (*prepared, error) {
	if !crewscope.ValidConversationID(id) {
		return nil, ErrInvalidID
	}
	if b.Allow == nil {
		return nil, ErrRefused
	}
	if err := b.Allow(ctx, scope, id); err != nil {
		return nil, err
	}
	held, err := b.Source.Held(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	zone, loc := zoneOf(opts.TimeZone)
	p := &prepared{scope: scope, id: id, held: held, zone: zone, loc: loc, now: b.clock()}
	p.memory = b.memoryOf(p, mem)
	return p, nil
}

// WriteConversation writes the zip of one conversation to w as it builds it, and returns
// its manifest. It returns ErrInvalidID, ErrRefused or ErrNotHeld before it writes anything.
func (b *Builder) WriteConversation(
	ctx context.Context, w io.Writer, scope crewscope.Scope, id string, opts Options,
) (Manifest, error) {
	p, err := b.prepare(ctx, scope, id, opts, &memoryRead{})
	if err != nil {
		return Manifest{}, err
	}
	return writeZip(ctx, w, p, false)
}

// ConversationManifest reads a conversation as WriteConversation would and returns its
// manifest without writing a zip or opening a workspace file: the selection report.
func (b *Builder) ConversationManifest(
	ctx context.Context, scope crewscope.Scope, id string, opts Options,
) (Manifest, error) {
	p, err := b.prepare(ctx, scope, id, opts, &memoryRead{})
	if err != nil {
		return Manifest{}, err
	}
	return writeZip(ctx, io.Discard, p, true)
}

// FileName is the download name of one conversation's zip.
func FileName(scope crewscope.Scope, id string) string {
	return fmt.Sprintf("%s-conversation-%s.zip", scope.Crew, id)
}

// BundleFileName is the download name of a zip of zips.
func BundleFileName(scope crewscope.Scope) string {
	return scope.Crew + "-conversations.zip"
}

// writeZip streams the conversation's zip: its readable and structured forms, the
// receipts, the artifacts, the memory facts, and last the manifest, which names whatever
// was left out along the way.
func writeZip(ctx context.Context, w io.Writer, p *prepared, dry bool) (Manifest, error) {
	zw := zip.NewWriter(w)
	m := Manifest{
		Kind: KindConversation, Origin: ompi.DefaultOrigin(p.scope), ExportedAt: p.now.Format(time.RFC3339),
		Namespace: p.scope.Namespace, Crew: p.scope.Crew, ConversationID: p.id, TimeZone: p.zone,
		Turns: len(p.held.Turns), LeftOut: append([]LeftOut{}, p.memory.leftOut...),
	}
	put := func(name string, data []byte) error { return putFile(zw, p.now, name, data) }
	if err := put(FileMarkdown, renderMarkdown(p)); err != nil {
		return Manifest{}, err
	}
	structured, err := renderJSON(p)
	if err != nil {
		return Manifest{}, err
	}
	if err := put(FileJSON, structured); err != nil {
		return Manifest{}, err
	}
	m.Parts = []Part{
		{Path: FileMarkdown, Status: StatusIncluded, Count: 1},
		{Path: FileJSON, Status: StatusIncluded, Count: 1},
	}
	receipts, err := writeReceipts(put, p)
	if err != nil {
		return Manifest{}, err
	}
	artifacts, left, err := writeArtifacts(ctx, zw, p, dry)
	if err != nil {
		return Manifest{}, err
	}
	m.LeftOut = append(m.LeftOut, left...)
	memory, err := writeMemory(put, p)
	if err != nil {
		return Manifest{}, err
	}
	m.Parts = append(m.Parts, receipts, artifacts, memory)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := put(FileManifest, raw); err != nil {
		return Manifest{}, err
	}
	return m, zw.Close()
}

func putFile(zw *zip.Writer, at time.Time, name string, data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
	if err != nil {
		return fmt.Errorf("zip %s: %w", name, err)
	}
	_, err = w.Write(data)
	return err
}

// unsafeName matches what a file name in the zip may not hold.
var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// safeName is one path element of the zip: a thread, agent or object name with anything
// outside letters, digits, '.', '_' and '-' replaced, and never "." or "..".
func safeName(s string) string {
	s = unsafeName.ReplaceAllString(s, "_")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

func writeReceipts(put func(string, []byte) error, p *prepared) (Part, error) {
	part := Part{Path: DirReceipts + "/", Status: StatusNotKept}
	threads := threadIDs(p.held.Turns)
	for _, t := range threads {
		raw, ok := p.held.Receipts[t]
		if !ok {
			continue
		}
		if err := put(DirReceipts+"/"+safeName(t)+".json", raw); err != nil {
			return part, err
		}
		part.Count++
	}
	switch {
	case part.Count == 0:
		part.Reason = "no receipt is stored for this conversation; " +
			"receipts are kept only while the discussion stream holds them"
	case part.Count < len(threads):
		part.Status = StatusIncluded
		part.Reason = fmt.Sprintf("%d of %d answers still have a stored receipt", part.Count, len(threads))
	default:
		part.Status = StatusIncluded
	}
	return part, nil
}

func threadIDs(turns []Turn) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, t := range turns {
		if _, dup := seen[t.ThreadID]; t.ThreadID == "" || dup {
			continue
		}
		seen[t.ThreadID] = struct{}{}
		out = append(out, t.ThreadID)
	}
	return out
}

func writeArtifacts(ctx context.Context, zw *zip.Writer, p *prepared, dry bool) (Part, []LeftOut, error) {
	part := Part{Path: DirArtifacts + "/"}
	var left []LeftOut
	files := append([]File(nil), p.held.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].Thread+"/"+files[i].Name < files[j].Thread+"/"+files[j].Name
	})
	names := map[string]struct{}{}
	for _, f := range files {
		name := uniqueName(names, DirArtifacts+"/"+safeName(f.Thread)+"/"+safeJoin(f.Name))
		if dry {
			part.Count++
			continue
		}
		switch err := copyFile(ctx, zw, p.now, name, f); {
		case err == nil:
			part.Count++
		case errors.Is(err, errOpen):
			left = append(left, LeftOut{Key: name, Reason: "the file could not be read from the workspace"})
		default:
			return part, nil, err
		}
	}
	if part.Count > 0 {
		part.Status = StatusIncluded
		return part, left, nil
	}
	part.Status = StatusExpired
	part.Reason = "no workspace file of this conversation is stored; " +
		"a thread's workspace is deleted when the thread closes"
	return part, left, nil
}

// uniqueName returns name, or name with a number before its end when another file already
// took it, which sanitizing can cause: "a b" and "a_b" are one name once cleaned.
func uniqueName(taken map[string]struct{}, name string) string {
	candidate := name
	for n := 2; ; n++ {
		if _, dup := taken[candidate]; !dup {
			taken[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name, n)
	}
}

// errOpen marks a file that could not be opened, which leaves it out and does not
// stop the export, as opposed to a failed write, which does.
var errOpen = errors.New("open")

func safeJoin(name string) string {
	out := ""
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '/' {
			if out != "" {
				out += "/"
			}
			out += safeName(name[start:i])
			start = i + 1
		}
	}
	return out
}

func copyFile(ctx context.Context, zw *zip.Writer, at time.Time, name string, f File) error {
	rc, err := f.Open(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errOpen, err)
	}
	defer func() { _ = rc.Close() }()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}
