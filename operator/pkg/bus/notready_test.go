/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bus

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
)

func TestAsNotReady_marksTheErrorsThatMeanTheBusIsNotThereYet(t *testing.T) {
	for _, cause := range []error{nats.ErrStreamNotFound, nats.ErrBucketNotFound, nats.ErrNoResponders,
		nats.ErrTimeout, nats.ErrConnectionClosed, nats.ErrNoServers, nats.ErrJetStreamNotEnabled} {
		wrapped := fmt.Errorf("reading tickets: %w", cause)
		got := AsNotReady(wrapped)
		if !errors.Is(got, ErrNotReady) || !errors.Is(got, cause) {
			t.Errorf("%v: not marked ready-wait, or the cause was lost: %v", cause, got)
		}
	}
}

func TestAsNotReady_leavesOtherErrorsAlone(t *testing.T) {
	other := errors.New("permission denied")
	if got := AsNotReady(other); got != other {
		t.Errorf("an unrelated error changed: %v", got)
	}
	if got := AsNotReady(nats.ErrKeyNotFound); errors.Is(got, ErrNotReady) {
		t.Error("a missing key is not a missing bus")
	}
	if AsNotReady(nil) != nil {
		t.Error("nil became an error")
	}
}
