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

package controller

import (
	"context"
	"slices"
	"sync"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// drainLog is the models each server is draining: awake, finishing the calls they hold,
// and about to be put to sleep. The engine adapter starts and ends a drain through the
// lifecycle server; the state publish reads the log, so the draining state holds across
// the periodic probes that run while a switch waits.
type drainLog struct {
	mu   sync.Mutex
	byOf map[string]map[string]struct{}
}

// Set marks the models draining, or no longer draining.
func (l *drainLog) Set(server string, models []string, draining bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byOf == nil {
		l.byOf = map[string]map[string]struct{}{}
	}
	for _, m := range models {
		if draining {
			if l.byOf[server] == nil {
				l.byOf[server] = map[string]struct{}{}
			}
			l.byOf[server][m] = struct{}{}
			continue
		}
		delete(l.byOf[server], m)
	}
	if len(l.byOf[server]) == 0 {
		delete(l.byOf, server)
	}
}

// For is the server's draining models; nil when there are none.
func (l *drainLog) For(server string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byOf[server]) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.byOf[server]))
	for m := range l.byOf[server] {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// Forget drops what was recorded for a server that no longer exists.
func (l *drainLog) Forget(server string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byOf, server)
}

// applyDraining publishes the models as draining: their modelStates entry says so, and
// they leave the loaded models, so a reader that does not know the state sees them as not
// awake and asks the operator to make them ready, which waits behind the switch.
func applyDraining(state *ServerState, draining []string) {
	if len(draining) == 0 {
		return
	}
	if state.ModelStates == nil {
		state.ModelStates = make(map[string]string, len(draining))
	}
	for _, m := range draining {
		state.ModelStates[m] = string(engine.StateDraining)
		delete(state.LoadedModelFootprintsMiB, m)
		delete(state.LoadedModelContextLengths, m)
	}
	state.LoadedModels = slices.DeleteFunc(slices.Clone(state.LoadedModels), func(m string) bool {
		return slices.Contains(draining, m)
	})
}

// SetDraining marks the models of the server draining, or no longer draining, and
// publishes the server state read now, so agents stop placing new calls on them before
// the switch starts to wait. A failed probe leaves the last published state as it was
// and the mark in place for the next publish.
func (r *ModelServerReconciler) SetDraining(ctx context.Context, server *aiv1alpha1.ModelServer, models []string, draining bool) {
	r.drains.Set(server.Name, models, draining)
	if _, err := r.probeSnapshot(ctx, server, r.refreshClient()); err != nil {
		logf.FromContext(ctx).V(1).Info("could not refresh the server state around a drain", "server", server.Name, "err", err)
		return
	}
	r.publishStateToKV(ctx, server)
}
