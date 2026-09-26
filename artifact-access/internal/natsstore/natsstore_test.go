package natsstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenRetainsConfigWithoutConnecting(t *testing.T) {
	s := Open("nats://example:4222", "my-bucket")
	if s == nil {
		t.Fatal("Open returned nil")
	}
	// Open must NOT dial - it only holds config; connections are per-Get.
	if s.url != "nats://example:4222" || s.bucket != "my-bucket" {
		t.Errorf("Open did not retain config: url=%q bucket=%q", s.url, s.bucket)
	}
}

func TestGetReturnsConnectErrorOnUnreachableServer(t *testing.T) {
	// Port 1 refuses connections, so the initial nats.Connect fails fast and Get
	// returns the wrapped "connect" error - and must not leak a reader.
	s := Open("nats://127.0.0.1:1", "bucket")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := s.Get(ctx, "crew/conv/thread/agent/tool-1")
	if rc != nil {
		_ = rc.Close()
		t.Fatal("no reader should be returned on a failed connect")
	}
	if err == nil {
		t.Fatal("expected a connect error against an unreachable server")
	}
	if !strings.Contains(err.Error(), "connect") {
		t.Errorf("expected a wrapped connect error, got %v", err)
	}
}
