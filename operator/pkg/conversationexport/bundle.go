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
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/ompi"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// ErrTooMany means a request asked for more than MaxBundle conversations.
var ErrTooMany = fmt.Errorf("ask for at most %d conversations at once", MaxBundle)

// Reasons the bundle manifest gives.
const (
	reasonInvalid = "not a valid conversation id"
	reasonRefused = "the caller may not export this conversation"
	reasonFailed  = "a store could not be read; ask again"
)

// WriteBundle writes a zip of zips to w: one conversation zip per conversation, each
// exactly the zip WriteConversation writes and able to stand alone, then a manifest.json
// listing every id asked for as included, refused, expired or failed. An id is never
// dropped: the caller's right to each is checked on its own, and a refusal or an
// expired conversation is listed, not skipped. Duplicate ids are asked once.
func (b *Builder) WriteBundle(
	ctx context.Context, w io.Writer, scope crewscope.Scope, ids []string, opts Options,
) (BundleManifest, error) {
	return b.bundle(ctx, w, scope, ids, opts, false)
}

// BundleManifest reads the conversations as WriteBundle would and returns its manifest
// without writing a zip or opening a workspace file: the selection report.
func (b *Builder) BundleManifest(
	ctx context.Context, scope crewscope.Scope, ids []string, opts Options,
) (BundleManifest, error) {
	return b.bundle(ctx, io.Discard, scope, ids, opts, true)
}

func (b *Builder) bundle(
	ctx context.Context, w io.Writer, scope crewscope.Scope, ids []string, opts Options, dry bool,
) (BundleManifest, error) {
	if len(ids) > MaxBundle {
		return BundleManifest{}, ErrTooMany
	}
	now := b.clock()
	bm := BundleManifest{
		Kind: KindBundle, Origin: ompi.DefaultOrigin(scope), ExportedAt: now.Format(time.RFC3339),
		Namespace: scope.Namespace, Crew: scope.Crew,
		Included: []Entry{}, Refused: []Entry{}, Expired: []Entry{}, Failed: []Entry{},
	}
	zw := zip.NewWriter(w)
	mem := &memoryRead{}
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if err := b.addConversation(ctx, zw, now, scope, id, opts, bundleRun{mem: mem, dry: dry}, &bm); err != nil {
			return bm, err
		}
	}
	raw, err := json.MarshalIndent(bm, "", "  ")
	if err != nil {
		return bm, err
	}
	if err := putFile(zw, now, FileManifest, raw); err != nil {
		return bm, err
	}
	return bm, zw.Close()
}

// bundleRun is what the conversations of one bundle share: the memory read once, and whether
// the run only builds the manifest.
type bundleRun struct {
	mem *memoryRead
	dry bool
}

// addConversation prepares one conversation and either writes its zip into the bundle or
// lists why it is not there. Only a failed write into the bundle is an error.
func (b *Builder) addConversation(
	ctx context.Context, zw *zip.Writer, now time.Time, scope crewscope.Scope, id string, opts Options,
	run bundleRun, bm *BundleManifest,
) error {
	p, err := b.prepare(ctx, scope, id, opts, run.mem)
	if err != nil {
		bm.list(id, err)
		return nil
	}
	file := FileName(scope, id)
	inner, err := zw.CreateHeader(&zip.FileHeader{Name: file, Method: zip.Store, Modified: now})
	if err != nil {
		return err
	}
	if _, err := writeZip(ctx, inner, p, run.dry); err != nil {
		return err
	}
	bm.Included = append(bm.Included, Entry{ConversationID: id, File: file})
	return nil
}

// list puts an id that could not be prepared in the list its error belongs to.
func (bm *BundleManifest) list(id string, err error) {
	switch {
	case errors.Is(err, ErrInvalidID):
		bm.Refused = append(bm.Refused, Entry{ConversationID: id, Reason: reasonInvalid})
	case errors.Is(err, ErrRefused):
		bm.Refused = append(bm.Refused, Entry{ConversationID: id, Reason: reasonRefused})
	case errors.Is(err, ErrNotHeld):
		bm.Expired = append(bm.Expired, Entry{ConversationID: id, Reason: ErrNotHeld.Error()})
	default:
		bm.Failed = append(bm.Failed, Entry{ConversationID: id, Reason: reasonFailed})
	}
}
