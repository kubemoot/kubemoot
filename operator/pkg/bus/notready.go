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

	"github.com/nats-io/nats.go"
)

// ErrNotReady marks a failure that means the bus is not there yet or not
// reachable right now: NATS cannot be reached, or a bucket the caller needs has not
// been created (the NATS bootstrap job creates the buckets, and on a fresh install
// it starts together with the operator). It is a state to wait out, never a
// verdict on the work that hit it.
var ErrNotReady = errors.New("the bus is not ready")

// notReadyCauses are the NATS client errors that mean the bus is not ready.
var notReadyCauses = []error{
	nats.ErrStreamNotFound, nats.ErrBucketNotFound, nats.ErrNoResponders,
	nats.ErrTimeout, nats.ErrConnectionClosed, nats.ErrDisconnected,
	nats.ErrNoServers, nats.ErrJetStreamNotEnabled, nats.ErrJetStreamNotEnabledForAccount,
	nats.ErrConnectionReconnecting, nats.ErrConnectionDraining,
}

// AsNotReady wraps err with ErrNotReady when it is one of the NATS errors that mean
// the bus is not ready, and returns every other error (and nil) unchanged.
func AsNotReady(err error) error {
	for _, cause := range notReadyCauses {
		if errors.Is(err, cause) {
			return fmt.Errorf("%w: %w", ErrNotReady, err)
		}
	}
	return err
}
