package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/runner"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/server"
)

func TestParseTransport(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", transportStdio, false},
		{"stdio", transportStdio, false},
		{"http", transportHTTP, false},
		{"sse", "", true},
		{"HTTP", "", true},
		{" http", "", true},
	}
	for _, c := range cases {
		got, err := parseTransport(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("parseTransport(%q) = %q, %v; want %q, error=%v", c.in, got, err, c.want, c.wantErr)
		}
	}
}

func TestServeListenerServesThenStops(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server.Server{Runner: &runner.Runner{Timeout: time.Second, MaxOutput: 1024, TempDir: t.TempDir()}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveListener(ctx, s, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + server.HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop after the context ended")
	}
}

func TestServeRejectsAnUnknownTransport(t *testing.T) {
	if err := serve(context.Background(), "sse", &server.Server{}, 0); err == nil {
		t.Fatal("want an error for an unknown transport")
	}
}

func TestServeHTTPFailsWhenThePortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	if err := serve(context.Background(), transportHTTP, &server.Server{}, port); err == nil {
		t.Fatal("want a bind error")
	}
}

func TestServeHTTPServesOnThePort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	s := &server.Server{Runner: &runner.Runner{Timeout: time.Second, MaxOutput: 1024, TempDir: t.TempDir()}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, transportHTTP, s, port) }()
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, server.ReadinessPath)
	var resp *http.Response
	for range 100 {
		if resp, err = http.Get(url); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown returned %v", err)
	}
}
