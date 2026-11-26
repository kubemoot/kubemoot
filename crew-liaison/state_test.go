package main

import (
	"encoding/base64"
	"testing"

	"github.com/go-logr/logr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/signin"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

func sealKeys() string {
	return "k1:" + base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func TestShareReplayStaysLocalWithoutASharedStore(t *testing.T) {
	cfg := &signin.DoorConfig{SealKeys: sealKeys()}
	op, _ := tokenstore.Config{Backend: tokenstore.BackendMemory}.Open()
	if err := shareReplay(cfg, op, logr.Discard()); err != nil || cfg.Replay != nil {
		t.Fatalf("memory backend: %v %v", cfg.Replay, err)
	}
	if err := shareReplay(cfg, nil, logr.Discard()); err != nil || cfg.Replay != nil {
		t.Fatalf("no state: %v %v", cfg.Replay, err)
	}
}

func TestShareReplayUsesNATS(t *testing.T) {
	env := natstest.Start(t, natstest.Options{JetStream: true})
	op, err := tokenstore.Config{Backend: tokenstore.BackendNATS, NATS: env.Config}.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	cfg := &signin.DoorConfig{SealKeys: sealKeys()}
	if err := shareReplay(cfg, op, logr.Discard()); err != nil || cfg.Replay == nil {
		t.Fatalf("nats backend: %v", err)
	}
	bad := &signin.DoorConfig{SealKeys: "not a key"}
	if err := shareReplay(bad, op, logr.Discard()); err == nil {
		t.Fatal("bad seal keys must fail")
	}
}
