package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/grants"
)

func TestRefuseGrantMapsEachCauseToItsStatus(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{errGrantConflict, http.StatusConflict},
		{grants.ErrFull, http.StatusServiceUnavailable},
		{errTokenExpired, http.StatusUnauthorized},
		{errors.New("unexpected"), http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		refuseGrant(rec, c.err)
		if rec.Code != c.code {
			t.Errorf("%v: status = %d, want %d", c.err, rec.Code, c.code)
		}
	}
}
