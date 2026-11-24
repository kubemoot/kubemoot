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

	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/ompi"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/secretscreen"
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
// refused. It is served behind gate: the caller must present a bearer token
// whose identity may get the Crew.
func (s *ReportServer) serveMemoryExport(w http.ResponseWriter, r *http.Request) {
	scope, _ := crewscope.New(r.PathValue("namespace"), r.PathValue("crew")) // validated by memoryExportAccess
	q := r.URL.Query()
	if q.Has("allowUnscreened") {
		http.Error(w, "an unscreened export is not available here; use kmctl", http.StatusBadRequest)
		return
	}
	if s.Memory == nil {
		http.Error(w, "crew memory export is not available", http.StatusServiceUnavailable)
		return
	}
	reader, err := s.Memory.OpenCrewMemory()
	if err != nil {
		failExport(w, scope, err, "crew memory is not reachable", http.StatusBadGateway)
		return
	}
	exp, files, err := ompi.ExportFiles(reader, scope, false, ompi.Options{ExportedAt: time.Now()})
	if err != nil {
		failExport(w, scope, err, "crew memory export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if q.Get("report") == valueTrue {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(memoryExportReport(scope, exp))
		return
	}
	writeMemoryExportZip(w, scope, files)
}

// writeMemoryExportZip answers with the export files as a zip download.
func writeMemoryExportZip(w http.ResponseWriter, scope crewscope.Scope, files map[string][]byte) {
	archive, err := ompi.Archive(files)
	if err != nil {
		failExport(w, scope, err, "crew memory export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-memory-ompi.zip", scope.Crew))
	_, _ = w.Write(archive)
}

// exportLog is the log of export failures, memory and conversation.
var exportLog = reportLog

// failExport answers with a fixed message and logs the cause redacted, for a memory or a
// conversation export. An error from the store can carry a store key, and a key that holds
// a credential is exactly what the export hides behind an opaque id, so the cause reaches
// neither the client nor the log as it is.
func failExport(w http.ResponseWriter, scope crewscope.Scope, cause error, message string, status int) {
	exportLog.Error(errors.New(secretscreen.Redact(cause.Error())), message,
		"namespace", scope.Namespace, "crew", scope.Crew)
	http.Error(w, message, status)
}

// memoryExportAccess is the access a memory export equals: get the Crew it is of.
func memoryExportAccess(r *http.Request) (authorizationv1.ResourceAttributes, error) {
	scope, err := crewscope.New(r.PathValue("namespace"), r.PathValue("crew"))
	if err != nil {
		return authorizationv1.ResourceAttributes{}, err
	}
	return crewAccess(scope), nil
}
