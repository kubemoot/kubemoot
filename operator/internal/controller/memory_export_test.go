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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func getExport(s *ReportServer, query string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /memory-export/{namespace}/{crew}", s.serveMemoryExport)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/memory-export/ns/c"+query, nil))
	return rec
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

func TestMemoryExportServesTheScreenedZipByDefault(t *testing.T) {
	rec := getExport(&ReportServer{Memory: fakeOpener{reader: crewFacts()}}, "")
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

func TestMemoryExportAllowUnscreenedKeepsTheCredentialFact(t *testing.T) {
	rec := getExport(&ReportServer{Memory: fakeOpener{reader: crewFacts()}}, "?allowUnscreened=true")
	if got := len(zipNames(t, rec.Body.Bytes())); got != 3 {
		t.Errorf("zip has %d files, want the manifest and two memories", got)
	}
}

func TestMemoryExportReportNamesWhatWasLeftOutNeverTheText(t *testing.T) {
	rec := getExport(&ReportServer{Memory: fakeOpener{reader: crewFacts()}}, "?report=true")
	var report MemoryExportReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Memories != 1 || !report.Screened || len(report.LeftOut) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.LeftOut[0].Reason, "credential") || strings.Contains(rec.Body.String(), "abcdefgh12345678") {
		t.Errorf("left out = %+v", report.LeftOut[0])
	}
}

func TestMemoryExportRefusesWhatItCannotServe(t *testing.T) {
	cases := map[string]struct {
		server *ReportServer
		path   string
		want   int
	}{
		"no memory store configured": {&ReportServer{}, "", http.StatusServiceUnavailable},
		"store unreachable":          {&ReportServer{Memory: fakeOpener{err: errors.New("down")}}, "", http.StatusBadGateway},
	}
	for name, c := range cases {
		if rec := getExport(c.server, c.path); rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, c.want)
		}
	}
}

func TestMemoryExportRejectsAnInvalidScope(t *testing.T) {
	s := &ReportServer{Memory: fakeOpener{reader: crewFacts()}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /memory-export/{namespace}/{crew}", s.serveMemoryExport)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/memory-export/n.s/c", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
