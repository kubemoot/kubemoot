/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"

	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

const (
	convOne          = "one"
	convThread       = "th-1"
	convTwo          = "two"
	caseNoAuthorizer = "no authorizer"
)

type convSource map[string]conversationexport.Held

func (c convSource) Held(_ context.Context, _ crewscope.Scope, id string) (conversationexport.Held, error) {
	if id == "down" {
		return conversationexport.Held{}, fmt.Errorf("read conv.ns.c.down: ghp_abcdefghijklmnopqrstuvwxyz0123 unreachable")
	}
	h, ok := c[id]
	if !ok {
		return conversationexport.Held{}, conversationexport.ErrNotHeld
	}
	return h, nil
}

type fakeConversations struct {
	source convSource
	mem    crewmemory.EntryReader
	err    error
}

func (f fakeConversations) OpenConversationExport() (*conversationexport.Builder, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &conversationexport.Builder{
		Source: f.source,
		Memory: func() (crewmemory.EntryReader, error) { return f.mem, nil },
		Now:    func() time.Time { return time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC) },
	}, nil
}

func heldConversation() conversationexport.Held {
	return conversationexport.Held{Turns: []conversationexport.Turn{
		{ThreadID: convThread, Question: "q", Answer: "a", AnsweredAt: time.Date(2026, 11, 20, 19, 3, 0, 0, time.UTC)},
	}}
}

func convServer(f fakeConversations) *ReportServer {
	return &ReportServer{Conversations: f, Auth: fakeAuth{allowed: goodToken}}
}

func getConversations(s *ReportServer, query string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/conversation-export/ns/c"+query, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func asDashboardConv(s *ReportServer, query string) *httptest.ResponseRecorder {
	return getConversations(s, query, headerAuthorization, "Bearer "+goodToken)
}

func twoConversations() fakeConversations {
	return fakeConversations{source: convSource{convOne: heldConversation(), convTwo: heldConversation()}, mem: memReader{}}
}

func TestConversationExportServesOneConversationAsAZip(t *testing.T) {
	rec := asDashboardConv(convServer(twoConversations()), "?id=one&timeZone=America/New_York")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("status = %d, type = %q, body = %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment; filename=c-conversation-one.zip" {
		t.Errorf("Content-Disposition = %q", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("an export must not be cached")
	}
	names := strings.Join(zipNames(t, rec.Body.Bytes()), " ")
	for _, want := range []string{"conversation.md", "conversation.json", "manifest.json"} {
		if !strings.Contains(names, want) {
			t.Errorf("zip lacks %s: %s", want, names)
		}
	}
}

func TestConversationExportOfSeveralIsAZipOfZipsWithAManifest(t *testing.T) {
	rec := asDashboardConv(convServer(twoConversations()), "?id=one&id=two&id=gone&id=bad.id")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Disposition") != "attachment; filename=c-conversations.zip" {
		t.Fatalf("status = %d, disposition = %q", rec.Code, rec.Header().Get("Content-Disposition"))
	}
	names := zipNames(t, rec.Body.Bytes())
	if strings.Join(names, " ") != "c-conversation-one.zip c-conversation-two.zip manifest.json" {
		t.Errorf("bundle files = %v", names)
	}
}

func TestConversationExportReportIsTheManifestAsJSON(t *testing.T) {
	rec := asDashboardConv(convServer(twoConversations()), "?id=one&report=true")
	var m conversationexport.Manifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.ConversationID != convOne || m.Turns != 1 {
		t.Errorf("status = %d, manifest = %+v, err = %v", rec.Code, m, err)
	}
	rec = asDashboardConv(convServer(twoConversations()), "?id=one&id=gone&report=true")
	var bm conversationexport.BundleManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &bm); err != nil || len(bm.Included) != 1 || len(bm.Expired) != 1 {
		t.Errorf("bundle report = %+v, err = %v", bm, err)
	}
}

func TestConversationExportAnswersTheErrorOfAnExportThatNeverStarted(t *testing.T) {
	long := strings.Repeat("&id=x", conversationexport.MaxBundle+1)
	cases := map[string]struct {
		query string
		want  int
	}{
		"no id":                   {"", http.StatusBadRequest},
		"an invalid id":           {"?id=a.b", http.StatusBadRequest},
		"too many ids":            {"?" + long[1:], http.StatusBadRequest},
		"a conversation not held": {"?id=gone", http.StatusNotFound},
		"an unscreened request":   {"?id=one&allowUnscreened=true", http.StatusBadRequest},
		"a store that is down":    {"?id=down", http.StatusInternalServerError},
	}
	for name, c := range cases {
		rec := asDashboardConv(convServer(twoConversations()), c.query)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, c.want, rec.Body.String())
		}
		if rec.Header().Get("Content-Disposition") != "" {
			t.Errorf("%s: an error carries a download header", name)
		}
	}
}

func TestConversationExportRefusalIsForbiddenAndListedInABundle(t *testing.T) {
	s := convServer(twoConversations())
	s.AllowConversation = func(_ context.Context, _ crewscope.Scope, id string) error {
		if id == convTwo {
			return conversationexport.ErrRefused
		}
		return nil
	}
	if rec := asDashboardConv(s, "?id=two"); rec.Code != http.StatusForbidden {
		t.Errorf("one refused conversation: status = %d, want 403", rec.Code)
	}
	rec := asDashboardConv(s, "?id=one&id=two&report=true")
	var bm conversationexport.BundleManifest
	_ = json.Unmarshal(rec.Body.Bytes(), &bm)
	if len(bm.Included) != 1 || len(bm.Refused) != 1 || bm.Refused[0].ConversationID != convTwo {
		t.Errorf("bundle = %+v, want two listed as refused", bm)
	}
}

func TestConversationExportRefusesACallerWithoutAccess(t *testing.T) {
	cases := map[string]struct {
		header []string
		want   int
	}{
		"no Authorization header":      {nil, http.StatusUnauthorized},
		"an empty bearer token":        {[]string{headerAuthorization, "Bearer "}, http.StatusUnauthorized},
		"a token that may not read it": {[]string{headerAuthorization, "Bearer other-pod"}, http.StatusForbidden},
	}
	for name, c := range cases {
		opened := false
		s := convServer(twoConversations())
		s.Conversations = openFunc(func() (*conversationexport.Builder, error) { opened = true; return nil, errors.New("no") })
		if rec := getConversations(s, "?id=one", c.header...); rec.Code != c.want || opened {
			t.Errorf("%s: status = %d, stores opened = %v, want %d and not opened", name, rec.Code, opened, c.want)
		}
	}
}

type openFunc func() (*conversationexport.Builder, error)

func (f openFunc) OpenConversationExport() (*conversationexport.Builder, error) { return f() }

func TestConversationExportFailsClosed(t *testing.T) {
	cases := map[string]*ReportServer{
		caseNoAuthorizer:         {Conversations: twoConversations()},
		"no conversation stores": {Auth: fakeAuth{allowed: goodToken}},
		"the stores are down":    {Conversations: fakeConversations{err: errors.New("down")}, Auth: fakeAuth{allowed: goodToken}},
		"access review is down":  {Conversations: twoConversations(), Auth: fakeAuth{err: errors.New("apiserver down")}},
	}
	want := map[string]int{
		caseNoAuthorizer: 503, "no conversation stores": 503, "the stores are down": 502, "access review is down": 503,
	}
	for name, s := range cases {
		if rec := asDashboardConv(s, "?id=one"); rec.Code != want[name] {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, want[name])
		}
	}
}

func TestConversationExportErrorsCarryNoKeyToTheClientOrTheLog(t *testing.T) {
	const keySecret = "ghp_abcdefghijklmnopqrstuvwxyz0123"
	var logged strings.Builder
	saved := exportLog
	exportLog = funcr.New(func(prefix, args string) { logged.WriteString(prefix + args + "\n") }, funcr.Options{})
	defer func() { exportLog = saved }()
	rec := asDashboardConv(convServer(twoConversations()), "?id=down")
	if strings.Contains(rec.Body.String(), keySecret) || strings.Contains(rec.Body.String(), "unreachable") {
		t.Errorf("the body carries the cause: %q", rec.Body.String())
	}
	if !strings.Contains(logged.String(), "conversation export failed") || strings.Contains(logged.String(), keySecret) {
		t.Errorf("log = %q, want the failure logged without the key", logged.String())
	}
}

func TestConversationExportRejectsAnInvalidScope(t *testing.T) {
	rec := httptest.NewRecorder()
	convServer(twoConversations()).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/conversation-export/n.s/c?id=one", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// failingSource serves a conversation whose artifact fails after the zip has started.
func TestConversationExportAbortsAResponseThatFailsAfterItStarted(t *testing.T) {
	held := heldConversation()
	noise := make([]byte, 1<<20) // incompressible, so the zip writer has sent bytes before the failure
	_, _ = rand.Read(noise)
	held.Files = []conversationexport.File{
		{Thread: convThread, Name: "obs/a", Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(noise)), nil
		}},
		{Thread: convThread, Name: "obs/z", Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(errReader{}), nil
		}},
	}
	s := convServer(fakeConversations{source: convSource{convOne: held}, mem: memReader{}})
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler so the client sees a failed transfer", r)
		}
	}()
	asDashboardConv(s, "?id="+convOne)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("object store went away") }
