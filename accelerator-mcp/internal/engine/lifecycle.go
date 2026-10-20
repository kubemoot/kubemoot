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
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// lifecycleTimeout is a safety net longer than the operator's own limit for a wake;
// the operator replies when the server reports the state.
const lifecycleTimeout = 31 * time.Minute

// requester is the part of a NATS connection a lifecycle request uses.
type requester interface {
	RequestWithContext(ctx context.Context, subj string, data []byte) (*nats.Msg, error)
}

// NATSLifecycle asks the operator, by request and reply on the model lifecycle
// subject, to wake or sleep a model, exactly as an agent does.
type NATSLifecycle struct {
	Conn requester
	// Agent names the caller in the operator's log.
	Agent string
}

// Ready asks the operator to make the model ready and returns when the server reports it awake.
func (l NATSLifecycle) Ready(ctx context.Context, server, model string) error {
	return l.ask(ctx, server, model, bus.ModelLifecycleRequestActionReady)
}

// Release asks the operator to put the model to sleep and returns when the server reports it so.
func (l NATSLifecycle) Release(ctx context.Context, server, model string) error {
	return l.ask(ctx, server, model, bus.ModelLifecycleRequestActionRelease)
}

func (l NATSLifecycle) ask(ctx context.Context, server, model string, action bus.ModelLifecycleRequestAction) error {
	if l.Conn == nil {
		return errors.New("NATS is not connected")
	}
	body, err := json.Marshal(bus.ModelLifecycleRequest{Action: action, Model: model, Agent: l.Agent})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout)
	defer cancel()
	msg, err := l.Conn.RequestWithContext(ctx, bus.ModelLifecycleSubject(server), body)
	if err != nil {
		return fmt.Errorf("asking the operator to %s %s: %w", action, model, err)
	}
	var reply bus.ModelLifecycleReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return fmt.Errorf("reading the operator's reply: %w", err)
	}
	if !reply.Ok {
		return fmt.Errorf("the operator could not %s %s: %s", action, model, reply.Error)
	}
	return nil
}
