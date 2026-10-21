package placement

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// service is a fake placement service: handler answers each path.
func service(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const (
	aliasDefault = "default"
	gpuOne       = "gpu1"
	keyWaitID    = "waitId"
	keyCode      = "code"
	keyReason    = "reason"
	codeNoFit    = "kubemoot_no_fit"
	keyKind      = "kind"
	aliasTriage  = "triage"
	modelBig     = "big"
	modelSmall   = "small"
)

var request = Request{Crew: "c", Agent: "a", ThreadID: "t", Alias: aliasDefault, Models: []string{"m"}, MayUnload: true}

func TestPlaceReturnsTheServicesPlacementAndSendsTheThread(t *testing.T) {
	var got Request
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/placement/v1/place" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeJSON(w, 200, Reply{Server: gpuOne, Endpoint: "http://e", Model: "m", Ticket: "tk", Queued: true})
	})
	rep, err := c.Place(context.Background(), request, nil)
	if err != nil || rep.Ticket != "tk" || rep.Server != gpuOne || rep.Degraded {
		t.Fatalf("%+v %v", rep, err)
	}
	if got.ThreadID != "t" || !got.Wait || got.Alias != "default" {
		t.Errorf("the request must carry thread, alias and wait: %+v", got)
	}
}

func TestANoFitCarriesItsKind(t *testing.T) {
	for name, body := range map[string]map[string]string{
		"a kind field":          {keyCode: codeNoFit, keyReason: "x", keyKind: KindModelTooLarge},
		"a reason that is kind": {keyCode: codeNoFit, keyReason: KindPromptTooLarge},
		"no kind":               {keyCode: codeNoFit, keyReason: "no server has room now"},
	} {
		c := service(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 409, body) })
		_, err := c.Place(context.Background(), request, nil)
		var nf *ErrNoFit
		if !errors.As(err, &nf) {
			t.Fatalf("%s: %v", name, err)
		}
		want := body[keyKind]
		if want == "" && body[keyReason] == KindPromptTooLarge {
			want = KindPromptTooLarge
		}
		if nf.Kind != want {
			t.Errorf("%s: kind %q, want %q", name, nf.Kind, want)
		}
	}
}

func TestAHeldCallIsFollowedToItsAnswerAndTheOwnerHearsOnce(t *testing.T) {
	var polls atomic.Int32
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			writeJSON(w, 202, map[string]string{keyWaitID: "w 1", keyReason: KindGPUBusy})
		case polls.Add(1) < 3:
			writeJSON(w, 202, map[string]string{keyWaitID: "w 1"})
		default:
			writeJSON(w, 200, Reply{Server: gpuOne, Model: "m", Ticket: "tk"})
		}
	})
	var heard []string
	rep, err := c.Place(context.Background(), request, func(reason string) { heard = append(heard, reason) })
	if err != nil || rep.Ticket != "tk" {
		t.Fatalf("%+v %v", rep, err)
	}
	if len(heard) != 1 || heard[0] != KindGPUBusy {
		t.Errorf("the owner must hear of the wait once with its reason: %v", heard)
	}
}

func TestAHeldCallThatEndsInNoFitIsANoFit(t *testing.T) {
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(w, 202, map[string]string{keyWaitID: "w"})
			return
		}
		writeJSON(w, 409, map[string]string{keyKind: KindGPUBusy, keyReason: "gave up"})
	})
	_, err := c.Place(context.Background(), request, nil)
	var nf *ErrNoFit
	if !errors.As(err, &nf) || nf.Kind != KindGPUBusy {
		t.Fatalf("%v", err)
	}
}

func TestTheCallersDisconnectEndsTheWait(t *testing.T) {
	released := make(chan struct{})
	held := make(chan struct{})
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(w, 202, map[string]string{keyWaitID: "w"})
			return
		}
		close(held)
		<-r.Context().Done() // the service holds the call until the caller leaves
		close(released)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Place(ctx, request, nil)
		done <- err
	}()
	<-held
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end when the caller left")
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not see the disconnect")
	}
}

func TestAnUnreachableOrFailingServiceIsUnavailable(t *testing.T) {
	down := NewClient("http://127.0.0.1:1")
	failing := service(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 503, map[string]string{}) })
	garbled := service(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) })
	for name, c := range map[string]*Client{"down": down, "5xx": failing, "garbled 200": garbled} {
		_, err := c.Place(context.Background(), request, nil)
		var un *ErrUnavailable
		if !errors.As(err, &un) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestABadRequestIsNotAnExcuseToDegrade(t *testing.T) {
	c := service(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 400, map[string]string{keyReason: "models is empty"})
	})
	_, err := c.Place(context.Background(), request, nil)
	var un *ErrUnavailable
	var nf *ErrNoFit
	if err == nil || errors.As(err, &un) || errors.As(err, &nf) {
		t.Fatalf("a 400 is a plain error: %v", err)
	}
}

func bound() Bound {
	return Bound{Default: "default", Aliases: map[string]Endpoint{
		aliasDefault: {Server: "gpu0", Endpoint: "http://bound", Model: modelBig},
		aliasTriage:  {Server: gpuOne, Endpoint: "http://small", Model: modelSmall},
	}}
}

func TestBoundPlacesOnTheOperatorsServerPerAlias(t *testing.T) {
	b := bound()
	for alias, want := range map[string]string{aliasDefault: modelBig, aliasTriage: modelSmall, "unknown": modelBig} {
		rep, err := b.Place(context.Background(), Request{Alias: alias}, nil)
		if err != nil || rep.Model != want || rep.Ticket != "" || rep.Reason != "bound" {
			t.Errorf("%s: %+v %v", alias, rep, err)
		}
	}
	if _, err := (Bound{}).Place(context.Background(), Request{}, nil); err == nil {
		t.Error("a bound placer with no endpoints must refuse")
	}
	if err := b.Release(context.Background(), Release{Ticket: "x"}); err != nil {
		t.Error(err)
	}
	if err := b.Plan(context.Background(), PlanRequest{}); err != nil {
		t.Error(err)
	}
}

func TestFallbackRunsDegradedOnlyWhenTheServiceIsUnavailable(t *testing.T) {
	f := Fallback{Primary: NewClient("http://127.0.0.1:1"), Bound: bound()}
	rep, err := f.Place(context.Background(), Request{Alias: aliasTriage}, nil)
	if err != nil || !rep.Degraded || rep.Model != modelSmall || rep.Ticket != "" {
		t.Fatalf("%+v %v", rep, err)
	}
	if err := f.Release(context.Background(), Release{}); err != nil {
		t.Errorf("a degraded call has nothing to release: %v", err)
	}

	noFit := service(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 409, map[string]string{keyKind: KindGPUBusy})
	})
	f = Fallback{Primary: noFit, Bound: bound()}
	_, err = f.Place(context.Background(), request, nil)
	var nf *ErrNoFit
	if !errors.As(err, &nf) {
		t.Fatalf("a no-fit is the service's decision and must not be overridden: %v", err)
	}

	f = Fallback{Primary: NewClient("http://127.0.0.1:1"), Bound: Bound{}}
	if _, err := f.Place(context.Background(), request, nil); err == nil {
		t.Error("with nothing bound there is no floor to fall back to")
	}
}

func TestReleaseReportsTheCall(t *testing.T) {
	var released Release
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&released)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Release(context.Background(), Release{Ticket: "tk", OK: true, CompletionTokens: 9, DurationMs: 40}); err != nil {
		t.Fatal(err)
	}
	if released.Ticket != "tk" || released.CompletionTokens != 9 || !released.OK {
		t.Errorf("%+v", released)
	}
	if err := c.Release(context.Background(), Release{}); err != nil {
		t.Errorf("an empty ticket is nothing to release: %v", err)
	}
	bad := service(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	if err := bad.Release(context.Background(), Release{Ticket: "x"}); err == nil {
		t.Error("a failed release is reported")
	}
}

func TestPlanSendsThePlan(t *testing.T) {
	var planned PlanRequest
	c := service(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&planned)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Plan(context.Background(), PlanRequest{ThreadID: "t", Models: []string{"m"}}); err != nil || planned.ThreadID != "t" {
		t.Errorf("%+v %v", planned, err)
	}
	bad := service(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	if err := bad.Plan(context.Background(), PlanRequest{}); err == nil {
		t.Error("a service without plans answers an error the caller may ignore")
	}
}
