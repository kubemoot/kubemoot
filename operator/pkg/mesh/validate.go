/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mesh

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Defaults the accessors apply to fields the file leaves at zero.
const (
	// DefaultProgressAck is a quarter of the operator's consumer AckWait (120 s), so a live turn
	// reports progress several times before JetStream would deliver its message again.
	DefaultProgressAck = 30 * time.Second
	// DefaultConcurrency is one turn at a time.
	DefaultConcurrency = 1
)

// Validate checks the whole contract and reports every problem it finds, so an operator or a
// person fixing the file sees them together.
func (c Contract) Validate() error {
	var errs []error
	if c.ContractVersion != ContractVersion {
		errs = append(errs, fmt.Errorf("contractVersion %q: this sidecar serves %q", c.ContractVersion, ContractVersion))
	}
	errs = append(errs, c.Identity.validate(), c.Bus.validate(), c.Turn.validate(),
		c.Models.validate(), c.Placement.validate(), c.Tools.validate(), c.Observability.validate())
	return errors.Join(errs...)
}

// validToken reports whether s is one NATS subject and key token: not empty, no separator,
// wildcard or space.
func validToken(field, s string) error {
	if s == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.ContainsAny(s, ".*> \t\r\n") {
		return fmt.Errorf("%s %q must be one NATS token (no dot, wildcard or space)", field, s)
	}
	return nil
}

func (i Identity) validate() error {
	errs := make([]error, 0, 3+len(i.Channels))
	errs = append(errs, validToken("identity.namespace", i.Namespace), validToken("identity.crew", i.Crew),
		validToken("identity.agent", i.Agent))
	for _, ch := range i.Channels {
		errs = append(errs, validToken("identity.channels entry", ch))
	}
	return errors.Join(errs...)
}

// validURL reports whether s is an absolute http or https URL.
func validURL(field, s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s %q must be an absolute http or https URL", field, s)
	}
	return nil
}

func (b Bus) validate() error {
	var errs []error
	if b.NATSURL == "" {
		errs = append(errs, errors.New("bus.natsURL is required"))
	}
	if (b.Consumer == "") != (b.Stream == "") {
		errs = append(errs, errors.New("bus.stream and bus.consumer are set together or not at all"))
	}
	if b.ProgressAckSeconds < 0 {
		errs = append(errs, fmt.Errorf("bus.progressAckSeconds %d must not be negative", b.ProgressAckSeconds))
	}
	return errors.Join(errs...)
}

// ProgressAck is how often a worked message is reported as still in progress.
func (b Bus) ProgressAck() time.Duration {
	if b.ProgressAckSeconds <= 0 {
		return DefaultProgressAck
	}
	return time.Duration(b.ProgressAckSeconds) * time.Second
}

func (t Turn) validate() error {
	errs := []error{validURL("turn.url", t.URL)}
	if t.Concurrency < 1 {
		errs = append(errs, fmt.Errorf("turn.concurrency %d must be at least 1", t.Concurrency))
	}
	if t.DeadlineSeconds < 1 {
		errs = append(errs, fmt.Errorf("turn.deadlineSeconds %d must be at least 1", t.DeadlineSeconds))
	}
	return errors.Join(errs...)
}

// Deadline is the safety net for one turn.
func (t Turn) Deadline() time.Duration { return time.Duration(t.DeadlineSeconds) * time.Second }

func (m Models) validate() error {
	if _, ok := m.Aliases["default"]; !ok {
		return errors.New("models.aliases needs a default alias")
	}
	var errs []error
	for name, a := range m.Aliases {
		errs = append(errs, a.validate("models.aliases."+name))
	}
	return errors.Join(errs...)
}

func (a Alias) validate(field string) error {
	var errs []error
	if len(a.Candidates) == 0 {
		errs = append(errs, fmt.Errorf("%s.candidates needs at least one model", field))
	}
	if f := a.Fallback; f != nil && (f.Server == "" || f.Endpoint == "" || f.Model == "") {
		errs = append(errs, fmt.Errorf("%s.fallback needs server, endpoint and model", field))
	}
	return errors.Join(errs...)
}

func (p Placement) validate() error {
	if p.ServiceURL == "" {
		return nil
	}
	return validURL("placement.serviceURL", p.ServiceURL)
}

func (t Tools) validate() error {
	var errs []error
	if t.Gateway != "" {
		errs = append(errs, validURL("tools.gateway", t.Gateway))
	}
	switch t.NativeTools {
	case "", NativeToolsNone, NativeToolsDeclared, NativeToolsAll:
	default:
		errs = append(errs, fmt.Errorf("tools.nativeTools %q must be none, declared or all", t.NativeTools))
	}
	return errors.Join(errs...)
}

func (o Observability) validate() error {
	if o.OTLPEndpoint == "" {
		return nil
	}
	return validURL("observability.otlpEndpoint", o.OTLPEndpoint)
}
