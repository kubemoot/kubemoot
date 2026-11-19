/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package engine

import "context"

// Drain is what the caller of an adapter gives it so a switch can stop new calls first and
// wait on the calls already placed. Both functions are optional.
type Drain struct {
	// Notify is told when the adapter starts and stops waiting for a server to go idle
	// before it puts it to sleep. models are every name the server answers to. It returns
	// when the draining state is published, so agents stop placing new calls on the
	// models before the adapter reads the count it waits on. An error from the start
	// call fails the switch, because nothing then stops new calls.
	Notify func(ctx context.Context, models []string, draining bool) error
	// Placed is the number of calls placed on the models that have not ended and are not
	// themselves waiting for this switch: calls the server may not have received yet,
	// which its own request count cannot show. An error fails the switch.
	Placed func(ctx context.Context, models []string) (int, error)
}

type drainKey struct{}

// WithDrain returns a context whose adapter calls use d.
func WithDrain(ctx context.Context, d Drain) context.Context {
	return context.WithValue(ctx, drainKey{}, d)
}

// NotifyDraining tells the context's Drain, when it has one, that the models started or
// stopped draining.
func NotifyDraining(ctx context.Context, models []string, draining bool) error {
	if d, ok := ctx.Value(drainKey{}).(Drain); ok && d.Notify != nil {
		return d.Notify(ctx, models, draining)
	}
	return nil
}

// PlacedCalls is the calls placed on the models and not yet ended, 0 when the context has
// no Drain to ask.
func PlacedCalls(ctx context.Context, models []string) (int, error) {
	if d, ok := ctx.Value(drainKey{}).(Drain); ok && d.Placed != nil {
		return d.Placed(ctx, models)
	}
	return 0, nil
}
