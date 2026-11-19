/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
)

const credentialValue = "Authorization: Bearer abcdefgh12345678"

type memReader map[string]string

func (m memReader) ListKVKeys(string) ([]string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m memReader) GetKVEntry(_, key string) (crewmemory.Entry, error) {
	return crewmemory.Entry{Value: []byte(m[key]), Revision: 3, Created: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}, nil
}

type openerFunc func() (crewmemory.EntryReader, error)

func (f openerFunc) OpenCrewMemory() (crewmemory.EntryReader, error) { return f() }

type fakeOpener struct {
	reader crewmemory.EntryReader
	err    error
}

func (f fakeOpener) OpenCrewMemory() (crewmemory.EntryReader, error) { return f.reader, f.err }

func crewFacts() memReader {
	return memReader{
		"ns.c.hardware.gpu0": `{"value":"RTX 5090","learnedBy":"a"}`,
		"ns.c.ops.login":     `{"value":"` + credentialValue + `","learnedBy":"a"}`,
	}
}

// fakeAuth admits the one token it knows and refuses the rest the way reviewAuthorizer does.
type fakeAuth struct {
	allowed string
	err     error
}

func (f fakeAuth) Authorize(_ context.Context, token string, _ authorizationv1.ResourceAttributes) error {
	switch {
	case f.err != nil:
		return f.err
	case token == "":
		return errUnauthenticated
	case token != f.allowed:
		return errForbidden
	}
	return nil
}

const goodToken = "dashboard-token"

func serverWith(memory memoryOpener) *ReportServer {
	return &ReportServer{Memory: memory, Auth: fakeAuth{allowed: goodToken}}
}

func getExport(s *ReportServer, query string, header ...string) *httptest.ResponseRecorder {
	mux := s.routes()
	req := httptest.NewRequest(http.MethodGet, "/memory-export/ns/c"+query, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func asDashboard(s *ReportServer, query string) *httptest.ResponseRecorder {
	return getExport(s, query, headerAuthorization, "Bearer "+goodToken)
}

func zipNames(t *testing.T, body []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func TestMemoryExportServesTheScreenedZip(t *testing.T) {
	rec := asDashboard(serverWith(fakeOpener{reader: crewFacts()}), "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("status = %d, type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got := len(zipNames(t, rec.Body.Bytes())); got != 2 {
		t.Errorf("zip has %d files, want the manifest and one memory", got)
	}
	if strings.Contains(rec.Body.String(), "abcdefgh12345678") {
		t.Error("the archive holds the credential")
	}
}

func TestMemoryExportRefusesAnUnscreenedRequestWhateverTheValue(t *testing.T) {
	for _, q := range []string{"?allowUnscreened=true", "?allowUnscreened=false", "?allowUnscreened=", "?report=true&allowUnscreened=true"} {
		rec := asDashboard(serverWith(fakeOpener{reader: crewFacts()}), q)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kmctl") {
			t.Errorf("%s: status = %d, body = %q, want 400 pointing to kmctl", q, rec.Code, rec.Body.String())
		}
	}
}

func TestMemoryExportReportNamesWhatWasLeftOutNeverTheText(t *testing.T) {
	rec := asDashboard(serverWith(fakeOpener{reader: crewFacts()}), "?report=true")
	var report MemoryExportReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Memories != 1 || len(report.LeftOut) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.LeftOut[0].Reason, "credential") || strings.Contains(rec.Body.String(), "abcdefgh12345678") {
		t.Errorf("left out = %+v", report.LeftOut[0])
	}
}

func TestMemoryExportRefusesACallerWithoutAccess(t *testing.T) {
	s := serverWith(fakeOpener{reader: crewFacts()})
	cases := map[string]struct {
		header []string
		want   int
	}{
		"no Authorization header":      {nil, http.StatusUnauthorized},
		"a non-bearer scheme":          {[]string{headerAuthorization, "Basic abc"}, http.StatusUnauthorized},
		"an empty bearer token":        {[]string{headerAuthorization, "Bearer "}, http.StatusUnauthorized},
		"a token that may not read it": {[]string{headerAuthorization, "Bearer other-pod"}, http.StatusForbidden},
	}
	for name, c := range cases {
		rec := getExport(s, "", c.header...)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, c.want)
		}
		if rec.Body.Len() > 0 && strings.Contains(rec.Body.String(), "RTX 5090") {
			t.Errorf("%s: the refusal carries memory", name)
		}
	}
}

func TestMemoryExportDoesNotReadMemoryBeforeTheCallerIsChecked(t *testing.T) {
	opened := false
	s := serverWith(openerFunc(func() (crewmemory.EntryReader, error) { opened = true; return crewFacts(), nil }))
	getExport(s, "")
	if opened {
		t.Error("the memory store was opened for an unauthenticated caller")
	}
}

func TestMemoryExportFailsClosed(t *testing.T) {
	cases := map[string]*ReportServer{
		"no authorizer":             {Memory: fakeOpener{reader: crewFacts()}},
		"no memory store":           {Auth: fakeAuth{allowed: goodToken}},
		"access review unavailable": {Memory: fakeOpener{reader: crewFacts()}, Auth: fakeAuth{err: errors.New("apiserver down")}},
	}
	for name, s := range cases {
		if rec := asDashboard(s, ""); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", name, rec.Code)
		}
	}
}

// failingEntry lists the key of a fact whose key holds a credential and fails reading it
// with an error that carries that key, as natskv and ReadFacts wrap one.
type failingEntry struct{ memReader }

func (failingEntry) GetKVEntry(_, key string) (crewmemory.Entry, error) {
	return crewmemory.Entry{}, fmt.Errorf("read %s: server went away", key)
}

func TestMemoryExportErrorsCarryNoKeyToTheClientOrTheLog(t *testing.T) {
	const keySecret = "ghp_abcdefghijklmnopqrstuvwxyz0123"
	var logged strings.Builder
	saved := memoryExportLog
	memoryExportLog = funcr.New(func(prefix, args string) { logged.WriteString(prefix + args + "\n") }, funcr.Options{})
	defer func() { memoryExportLog = saved }()

	reader := failingEntry{memReader{"ns.c.ops." + keySecret: `{"value":"x","learnedBy":"a"}`}}
	opens := map[string]memoryOpener{
		"reading a fact fails":    fakeOpener{reader: reader},
		"opening the store fails": fakeOpener{err: fmt.Errorf("bucket for key %s: down", keySecret)},
	}
	for name, opener := range opens {
		for _, q := range []string{"", "?report=true"} {
			rec := asDashboard(serverWith(opener), q)
			if rec.Code < 500 {
				t.Fatalf("%s%s: status = %d, want a server error", name, q, rec.Code)
			}
			if body := rec.Body.String(); strings.Contains(body, keySecret) || strings.Contains(body, "server went away") {
				t.Errorf("%s%s: the body carries the cause: %q", name, q, body)
			}
		}
	}
	if !strings.Contains(logged.String(), "crew memory") {
		t.Error("the failure was not logged")
	}
	if strings.Contains(logged.String(), keySecret) {
		t.Errorf("the log carries the key: %s", logged.String())
	}
}

func TestMemoryExportReportsAnUnreachableStore(t *testing.T) {
	if rec := asDashboard(serverWith(fakeOpener{err: errors.New("down")}), ""); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestMemoryExportRejectsAnInvalidScope(t *testing.T) {
	s := serverWith(fakeOpener{reader: crewFacts()})
	mux := s.routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/memory-export/n.s/c", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
