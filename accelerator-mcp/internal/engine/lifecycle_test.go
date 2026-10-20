/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

type fakeConn struct {
	subject string
	request bus.ModelLifecycleRequest
	reply   string
	err     error
}

func (c *fakeConn) RequestWithContext(_ context.Context, subj string, data []byte) (*nats.Msg, error) {
	c.subject = subj
	_ = json.Unmarshal(data, &c.request)
	if c.err != nil {
		return nil, c.err
	}
	return &nats.Msg{Data: []byte(c.reply)}, nil
}

func TestTheLifecycleAsksTheOperatorOnTheSubjectAgentsUse(t *testing.T) {
	conn := &fakeConn{reply: `{"ok":true,"state":"awake"}`}
	l := NATSLifecycle{Conn: conn, Agent: "accelerator-mcp"}
	if err := l.Ready(context.Background(), "vllm.rig1", "qwen3:14b"); err != nil {
		t.Fatal(err)
	}
	if conn.subject != bus.ModelLifecycleSubject("vllm.rig1") || conn.subject != "kubemoot.server.model.vllm_2erig1" {
		t.Errorf("subject = %q", conn.subject)
	}
	if conn.request.Action != bus.ModelLifecycleRequestActionReady || conn.request.Model != "qwen3:14b" ||
		conn.request.Agent != "accelerator-mcp" {
		t.Errorf("request = %+v", conn.request)
	}
	if err := l.Release(context.Background(), "p", "m"); err != nil || conn.request.Action != bus.ModelLifecycleRequestActionRelease {
		t.Errorf("release: %v %+v", err, conn.request)
	}
}

func TestARefusalOrAFailureToAskIsAnErrorWithTheReason(t *testing.T) {
	refused := NATSLifecycle{Conn: &fakeConn{reply: `{"ok":false,"error":"no vLLM server serves model m"}`}}
	if err := refused.Ready(context.Background(), "p", "m"); err == nil || !strings.Contains(err.Error(), "no vLLM server serves model m") {
		t.Errorf("err = %v", err)
	}
	down := NATSLifecycle{Conn: &fakeConn{err: errors.New("no responders")}}
	if err := down.Ready(context.Background(), "p", "m"); err == nil || !strings.Contains(err.Error(), "no responders") {
		t.Errorf("err = %v", err)
	}
	garbled := NATSLifecycle{Conn: &fakeConn{reply: "nope"}}
	if err := garbled.Ready(context.Background(), "p", "m"); err == nil {
		t.Error("an unreadable reply must fail")
	}
	if err := (NATSLifecycle{}).Ready(context.Background(), "p", "m"); err == nil {
		t.Error("no connection must fail")
	}
}
