/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/ompi"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// memoryOpener opens a read-only reader of the crew memory store.
type memoryOpener interface {
	OpenCrewMemory() (crewmemory.EntryReader, error)
}

// LeftOutEntry is one entry an export did not include: its key, or an opaque id
// when the key itself holds a credential, and the reason. It never carries a
// fact's text.
type LeftOutEntry struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// MemoryExportReport is the selection report of one export: how many memory
// objects it holds and which entries were left out.
type MemoryExportReport struct {
	Namespace string         `json:"namespace"`
	Crew      string         `json:"crew"`
	Memories  int            `json:"memories"`
	LeftOut   []LeftOutEntry `json:"leftOut"`
}

// memoryExportReport summarizes an export for display.
func memoryExportReport(scope crewscope.Scope, exp ompi.Export) MemoryExportReport {
	left := make([]LeftOutEntry, 0, len(exp.Skipped))
	for _, o := range exp.Skipped {
		left = append(left, LeftOutEntry{Key: o.Key, Reason: o.Reason})
	}
	return MemoryExportReport{
		Namespace: scope.Namespace, Crew: scope.Crew, Memories: len(exp.Memories), LeftOut: left,
	}
}

// serveMemoryExport answers GET /memory-export/{namespace}/{crew}: the crew's
// memory as the same OMPI export ompi-export writes, as a zip, or with
// report=true the selection report as JSON. The export is always screened: a
// fact that holds a credential is left out, and a request for allowUnscreened is
// refused. The caller must present a bearer token whose identity may get the Crew.
func (s *ReportServer) serveMemoryExport(w http.ResponseWriter, r *http.Request) {
	scope, err := crewscope.New(r.PathValue("namespace"), r.PathValue("crew"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if q.Has("allowUnscreened") {
		http.Error(w, "an unscreened export is not available here; use kmctl", http.StatusBadRequest)
		return
	}
	if !s.authorizeMemoryExport(w, r, scope) {
		return
	}
	reader, err := s.Memory.OpenCrewMemory()
	if err != nil {
		reportLog.Error(err, "crew memory unreachable", "namespace", scope.Namespace, "crew", scope.Crew)
		http.Error(w, "crew memory is not reachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	exp, files, err := ompi.ExportFiles(reader, scope, false, ompi.Options{ExportedAt: time.Now()})
	if err != nil {
		reportLog.Error(err, "memory export failed", "namespace", scope.Namespace, "crew", scope.Crew)
		http.Error(w, "memory export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if q.Get("report") == valueTrue {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(memoryExportReport(scope, exp))
		return
	}
	archive, err := ompi.Archive(files)
	if err != nil {
		http.Error(w, "memory export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-memory-ompi.zip", scope.Crew))
	_, _ = w.Write(archive)
}

// authorizeMemoryExport answers the request itself and returns false when the export
// may not be served: no memory store or no authorizer configured (fail closed), no
// valid token (401), an identity that may not read the crew (403), or a review failure.
func (s *ReportServer) authorizeMemoryExport(w http.ResponseWriter, r *http.Request, scope crewscope.Scope) bool {
	if s.Memory == nil || s.MemoryAuth == nil {
		http.Error(w, "crew memory export is not available", http.StatusServiceUnavailable)
		return false
	}
	err := s.MemoryAuth.Authorize(r.Context(), bearerToken(r), scope)
	switch {
	case err == nil:
		return true
	case errors.Is(err, errUnauthenticated):
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, err.Error(), http.StatusUnauthorized)
	case errors.Is(err, errForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		reportLog.Error(err, "memory export authorization failed", "namespace", scope.Namespace, "crew", scope.Crew)
		http.Error(w, "could not check the caller's access", http.StatusServiceUnavailable)
	}
	return false
}
