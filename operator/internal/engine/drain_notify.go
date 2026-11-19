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

// DrainNotifier is told when an adapter starts and stops waiting for a server to go
// idle before it puts it to sleep. models are every name the server answers to. The
// caller publishes the draining state, so agents stop placing new calls on the
// models before the adapter reads the count it waits on. It returns when the state
// is published.
type DrainNotifier func(ctx context.Context, models []string, draining bool)

type drainNotifierKey struct{}

// WithDrainNotifier returns a context whose adapter calls report draining to notify.
func WithDrainNotifier(ctx context.Context, notify DrainNotifier) context.Context {
	return context.WithValue(ctx, drainNotifierKey{}, notify)
}

// NotifyDraining tells the context's DrainNotifier, when it has one, that the models
// started or stopped draining.
func NotifyDraining(ctx context.Context, models []string, draining bool) {
	if notify, ok := ctx.Value(drainNotifierKey{}).(DrainNotifier); ok && notify != nil {
		notify(ctx, models, draining)
	}
}
