package obs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The handler keeps the status the wrapped handler wrote and still reaches the real writer.
func TestHandlerForwardsStatusAndExposesTheWriter(t *testing.T) {
	var unwrapped http.ResponseWriter
	h := Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unwrapped = w.(interface{ Unwrap() http.ResponseWriter }).Unwrap()
		w.WriteHeader(http.StatusTeapot)
	}), "test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if unwrapped != http.ResponseWriter(rec) {
		t.Fatal("Unwrap did not return the wrapped writer")
	}
}
