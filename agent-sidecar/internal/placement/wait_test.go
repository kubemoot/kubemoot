package placement

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestAWaitTheServiceForgotDegradesInsteadOfFailing(t *testing.T) {
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(w, 202, map[string]string{keyWaitID: "gone"})
			return
		}
		w.WriteHeader(http.StatusGone)
	})
	f := Fallback{Primary: c, Bound: bound()}
	rep, err := f.Place(context.Background(), request, nil)
	if err != nil || !rep.Degraded {
		t.Fatalf("a placement the service lost must run on the bound server: %+v %v", rep, err)
	}
}

func TestAServiceThatAnswers202AtOnceIsNotAskedInATightLoop(t *testing.T) {
	var asks atomic.Int32
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(w, 202, map[string]string{keyWaitID: "w"})
			return
		}
		if asks.Add(1) >= 3 {
			writeJSON(w, 200, Reply{Server: gpuOne, Model: "m", Ticket: "tk"})
			return
		}
		writeJSON(w, 202, map[string]string{keyWaitID: "w"})
	})
	started := time.Now()
	if _, err := c.Place(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); asks.Load() != 3 || took < 3*waitPause {
		t.Fatalf("%d asks took %v; each ask after a 202 waits at least %v", asks.Load(), took, waitPause)
	}
}
