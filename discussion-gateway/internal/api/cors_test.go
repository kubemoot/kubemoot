/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
)

const (
	headerAllowOrigin      = "Access-Control-Allow-Origin"
	headerAllowCredentials = "Access-Control-Allow-Credentials"
	appOrigin              = "https://app.example.com"
)

func streamHeaders(t *testing.T, h *Handler, origin string) http.Header {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	if _, ok := h.sseEmitter(rec, req); !ok {
		t.Fatal("recorder must be able to stream")
	}
	return rec.Header()
}

func newCORSHandler() *Handler { return NewHandler(natsclient.NewClient(""), "ns") }

func TestNoAllowListSendsNoCORSHeaders(t *testing.T) {
	h := streamHeaders(t, newCORSHandler(), "https://evil.example")
	for _, name := range []string{headerAllowOrigin, headerAllowCredentials, "Vary"} {
		if got := h.Get(name); got != "" {
			t.Errorf("%s = %q, want none", name, got)
		}
	}
}

func TestAllowedOriginIsAnsweredWithoutCredentials(t *testing.T) {
	h := streamHeaders(t, newCORSHandler().AllowOrigins([]string{appOrigin}), appOrigin)
	if got := h.Get(headerAllowOrigin); got != appOrigin {
		t.Errorf("allow-origin = %q, want %q", got, appOrigin)
	}
	if got := h.Get(headerAllowCredentials); got != "" {
		t.Errorf("credentials = %q, want none", got)
	}
	if got := h.Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestOriginOffTheListIsNotReflected(t *testing.T) {
	handler := newCORSHandler().AllowOrigins([]string{appOrigin})
	for _, origin := range []string{"https://evil.example", "https://app.example.com.evil.example", "http://app.example.com", ""} {
		h := streamHeaders(t, handler, origin)
		if got := h.Get(headerAllowOrigin); got != "" {
			t.Errorf("origin %q got allow-origin %q, want none", origin, got)
		}
		if got := h.Get(headerAllowCredentials); got != "" {
			t.Errorf("origin %q got credentials %q, want none", origin, got)
		}
	}
}

func TestMalformedAllowListEntriesAreIgnored(t *testing.T) {
	handler := newCORSHandler().AllowOrigins([]string{"", "*", "https://*.example.com", "app.example.com", "https://app.example.com/path", "ftp://x.example"})
	if len(handler.allowedOrigins) != 0 {
		t.Fatalf("allowed = %v, want none", handler.allowedOrigins)
	}
	if got := streamHeaders(t, handler, "*").Get(headerAllowOrigin); got != "" {
		t.Errorf("allow-origin = %q, want none", got)
	}
}

func TestSeveralOriginsAreAllAnswered(t *testing.T) {
	handler := newCORSHandler().AllowOrigins([]string{"https://a.example.com", "http://localhost:5173"})
	for _, origin := range []string{"https://a.example.com", "http://localhost:5173"} {
		if got := streamHeaders(t, handler, origin).Get(headerAllowOrigin); got != origin {
			t.Errorf("allow-origin = %q, want %q", got, origin)
		}
	}
}
