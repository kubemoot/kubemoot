package placement

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func service(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var released []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /placement/v1/place", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Case") {
		default:
			_, _ = w.Write([]byte(`{"server":"gpu-1","endpoint":"http://x","model":"m","reason":"warm","ticket":"t1","evicted":[]}`))
		}
	})
	mux.HandleFunc("POST /placement/v1/release", func(w http.ResponseWriter, _ *http.Request) {
		released = append(released, "t1")
		w.WriteHeader(http.StatusNoContent)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return NewClient(s.URL), &released
}

func TestPlaceAndRelease(t *testing.T) {
	c, released := service(t)
	got, err := c.Place(context.Background(), Request{Crew: "c", Agent: "a", Models: []string{"m"}})
	if err != nil || got.Ticket != "t1" || got.Server != "gpu-1" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := c.Release(context.Background(), got.Ticket); err != nil || len(*released) != 1 {
		t.Fatalf("release: %v %v", err, *released)
	}
}

func TestBoundPlacesOnTheBoundServerWithoutATicket(t *testing.T) {
	b := Bound{Server: "gpu-1", Endpoint: "http://x"}
	got, err := b.Place(context.Background(), Request{Models: []string{"a", "b"}})
	if err != nil || got.Model != "a" || got.Ticket != "" || got.Endpoint != "http://x" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := b.Release(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Place(context.Background(), Request{}); err == nil {
		t.Fatal("no models should be no fit")
	}
}

func TestReleaseOfNoTicketIsANoop(t *testing.T) {
	c, released := service(t)
	if err := c.Release(context.Background(), ""); err != nil || len(*released) != 0 {
		t.Fatalf("%v %v", err, *released)
	}
}

func TestConflictIsNoFit(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"kubemoot_no_fit","reason":"busy"}`))
	}))
	defer s.Close()
	_, err := NewClient(s.URL).Place(context.Background(), Request{Models: []string{"m"}})
	var nf *ErrNoFit
	if !errors.As(err, &nf) || nf.Reason != "busy" {
		t.Fatalf("%v", err)
	}
}

func TestServerErrorAndUnreachable(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", 500) }))
	if _, err := NewClient(s.URL).Place(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error")
	}
	s.Close()
	if _, err := NewClient(s.URL).Place(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error for an unreachable service")
	}
}
