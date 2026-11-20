package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
)

func newTestMux() *http.ServeMux {
	h := NewHandler(natsclient.NewClient(""), "team-a")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func TestPostDiscussionRejectsCrewThatIsNotOneSubjectToken(t *testing.T) {
	mux := newTestMux()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discussions/pi.lot", strings.NewReader(`{"message":"hi"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPostDiscussionValidCrewReachesNATSCheck(t *testing.T) {
	mux := newTestMux()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discussions/pilot", strings.NewReader(`{"message":"hi"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (NATS not configured)", rec.Code)
	}
}

func TestStreamDiscussionRejectsCrewThatIsNotOneSubjectToken(t *testing.T) {
	mux := newTestMux()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discussions/pilot%3E/conv-1/stream", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPostDiscussionRefusesInvalidConversationID(t *testing.T) {
	mux := newTestMux()
	for _, id := range []string{"ns-b.crew-b.someId", "a.b", "*", "a>", "a b", strings.Repeat("x", 129)} {
		body := `{"message":"hi","conversationId":"` + id + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discussions/pilot", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid conversationId") {
			t.Errorf("id %q: status %d body %q, want 400 invalid conversationId", id, rec.Code, rec.Body.String())
		}
	}
}

func TestPostDiscussionAcceptsValidConversationID(t *testing.T) {
	mux := newTestMux()
	for _, id := range []string{"conv-1", "A_b-9", strings.Repeat("x", 128)} {
		body := `{"message":"hi","conversationId":"` + id + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discussions/pilot", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("id %q: status %d, want 503 (past validation, NATS not configured)", id, rec.Code)
		}
	}
}

func TestStreamDiscussionRefusesInvalidConversationID(t *testing.T) {
	mux := newTestMux()
	for _, id := range []string{"a.b", "a%3Eb", "a%2Ab", strings.Repeat("x", 129)} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/discussions/pilot/"+id+"/stream", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q: status %d, want 400", id, rec.Code)
		}
	}
}
