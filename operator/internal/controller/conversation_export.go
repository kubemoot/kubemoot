/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// conversationOpener gives a builder of conversation exports over the stores.
type conversationOpener interface {
	OpenConversationExport() (*conversationexport.Builder, error)
}

// mayExportConversation is the one place that decides whether the caller may export a
// conversation. A conversation has no owner yet, so the rule is the access the route already
// checked: the caller's token may get the Crew, as for a memory export. Identity phase 1
// replaces this function with a check of the conversation's owner, and the export builder
// and the route do not change.
func mayExportConversation(context.Context, crewscope.Scope, string) error { return nil }

// downloadWriter sends the download headers with the first byte, so an export that is
// refused or fails before it writes can still answer with a plain error.
type downloadWriter struct {
	http.ResponseWriter
	filename string
	started  bool
}

func (d *downloadWriter) Write(p []byte) (int, error) {
	if !d.started {
		d.started = true
		h := d.Header()
		h.Set("Content-Type", "application/zip")
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", d.filename))
	}
	return d.ResponseWriter.Write(p)
}

// serveConversationExport answers GET /conversation-export/{namespace}/{crew}?id=...: one
// conversation as a zip, or, for several id parameters, a zip of zips with a manifest of
// what is included, refused, expired or failed. With report=true it answers the manifest as
// JSON and no zip. The zip is built as it is sent and never stored. It is served behind
// requireAccess, as the memory export is, and its memory part is the memory export's own
// screened OMPI files: a request for allowUnscreened is refused.
func (s *ReportServer) serveConversationExport(w http.ResponseWriter, r *http.Request) {
	scope, _ := crewscope.New(r.PathValue("namespace"), r.PathValue("crew")) // validated by memoryExportAccess
	ids, ok := s.exportRequest(w, r)
	if !ok {
		return
	}
	b, err := s.Conversations.OpenConversationExport()
	if err != nil {
		failExport(w, scope, err, "the conversation stores are not reachable", http.StatusBadGateway)
		return
	}
	b.Allow = s.AllowConversation
	if b.Allow == nil {
		b.Allow = mayExportConversation
	}
	opts := conversationexport.Options{TimeZone: r.URL.Query().Get("timeZone")}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("report") == valueTrue {
		body, err := conversationReport(r.Context(), b, scope, ids, opts)
		answerConversationExport(w, scope, err, body)
		return
	}
	sendConversationZip(r.Context(), w, b, scope, ids, opts)
}

// exportRequest checks what the request names before any store is opened: no unscreened
// export, at least one id, and an export service to ask. It answers the error itself.
func (s *ReportServer) exportRequest(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	q := r.URL.Query()
	switch {
	case q.Has("allowUnscreened"):
		http.Error(w, "an unscreened export is not available here", http.StatusBadRequest)
	case len(q["id"]) == 0:
		http.Error(w, "id is required: the conversation to export, repeated for several", http.StatusBadRequest)
	case s.Conversations == nil:
		http.Error(w, "conversation export is not available", http.StatusServiceUnavailable)
	default:
		return q["id"], true
	}
	return nil, false
}

// sendConversationZip streams the zip of one conversation, or the zip of zips of several.
func sendConversationZip(
	ctx context.Context, w http.ResponseWriter, b *conversationexport.Builder, scope crewscope.Scope,
	ids []string, opts conversationexport.Options,
) {
	name := conversationexport.BundleFileName(scope)
	build := func(dst io.Writer) error {
		_, err := b.WriteBundle(ctx, dst, scope, ids, opts)
		return err
	}
	if len(ids) == 1 {
		name = conversationexport.FileName(scope, ids[0])
		build = func(dst io.Writer) error {
			_, err := b.WriteConversation(ctx, dst, scope, ids[0], opts)
			return err
		}
	}
	dw := &downloadWriter{ResponseWriter: w, filename: name}
	switch err := build(dw); {
	case err != nil && !dw.started:
		answerConversationExport(w, scope, err, nil)
	case err != nil:
		// The zip is under way and its status is sent. The cause is logged redacted, and the
		// response is aborted so the client sees a failed transfer, not a complete-looking 200.
		failExport(nopWriter{}, scope, err, "the conversation export was cut short", http.StatusInternalServerError)
		panic(http.ErrAbortHandler)
	}
}

// conversationReport is the manifest of the export without its zip: the one conversation's
// manifest, or the bundle's.
func conversationReport(
	ctx context.Context, b *conversationexport.Builder, scope crewscope.Scope, ids []string, opts conversationexport.Options,
) (any, error) {
	if len(ids) == 1 {
		return b.ConversationManifest(ctx, scope, ids[0], opts)
	}
	return b.BundleManifest(ctx, scope, ids, opts)
}

// answerConversationExport answers a report, or the error that stopped the export before
// it wrote: an id that is not valid or too many ids is 400, a refusal 403, a conversation
// that is not held 404, and anything else a fixed message with the cause logged redacted.
func answerConversationExport(w http.ResponseWriter, scope crewscope.Scope, err error, body any) {
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	case errors.Is(err, conversationexport.ErrInvalidID), errors.Is(err, conversationexport.ErrTooMany):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, conversationexport.ErrRefused):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, conversationexport.ErrNotHeld):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		failExport(w, scope, err, "conversation export failed", http.StatusInternalServerError)
	}
}

// nopWriter takes the status and body failExport would send to a response that is already
// under way, so only the redacted log line remains.
type nopWriter struct{}

func (nopWriter) Header() http.Header         { return http.Header{} }
func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriter) WriteHeader(int)             {}
