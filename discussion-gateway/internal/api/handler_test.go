package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	natsclient "github.com/javajon/kubemoot/discussion-gateway/internal/nats"
)

func newTestMux(namespace string) *http.ServeMux {
	h := NewHandler(natsclient.NewClient(""), namespace)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func TestPostDiscussionRejectsCrewThatIsNotOneSubjectToken(t *testing.T) {
	mux := newTestMux("team-a")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discussions/pi.lot", strings.NewReader(`{"message":"hi"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPostDiscussionValidCrewReachesNATSCheck(t *testing.T) {
	mux := newTestMux("team-a")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discussions/pilot", strings.NewReader(`{"message":"hi"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (NATS not configured)", rec.Code)
	}
}

func TestStreamDiscussionRejectsCrewThatIsNotOneSubjectToken(t *testing.T) {
	mux := newTestMux("team-a")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discussions/pilot%3E/conv-1/stream", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
