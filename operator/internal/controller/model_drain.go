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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
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
// publishes the server state, so agents stop placing new calls on them before the switch
// starts to wait. The publish does not depend on a fresh probe: a probe that fails leaves
// the last snapshot in place and the state is still written, with the mark. The error is
// the failure to write it; the caller fails the switch on it when starting, and has
// nothing to undo when clearing (the next publish clears the mark too).
func (r *ModelServerReconciler) SetDraining(ctx context.Context, server *aiv1alpha1.ModelServer, models []string, draining bool) error {
	r.drains.Set(server.Name, models, draining)
	if _, err := r.probeSnapshot(ctx, server, r.refreshClient()); err != nil {
		logf.FromContext(ctx).V(1).Info("could not probe the server around a drain; publishing its last snapshot", "server", server.Name, "err", err)
	}
	return r.publishState(ctx, server)
}

// ticketValue is the part of a placement ticket the drain reads.
type ticketValue struct {
	ModelName string `json:"modelName"`
}

// PlacedCalls is the placement tickets held on the server for any of the models: calls the
// scheduler placed and that have not ended, including ones the engine has not received
// yet. A ticket without a model name cannot be attributed and is not counted. With no KV
// configured there are none.
func (r *ModelServerReconciler) PlacedCalls(_ context.Context, server *aiv1alpha1.ModelServer, models []string) (int, error) {
	kv := r.store()
	if kv == nil {
		return 0, nil
	}
	keys, err := kv.ListKVKeys(bus.ServerTicketsBucket)
	if err != nil {
		return 0, fmt.Errorf("listing placement tickets: %w", err)
	}
	prefix := bus.ServerTicketPrefix(server.Name)
	count := 0
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		raw, err := kv.GetKVValue(bus.ServerTicketsBucket, k)
		if err != nil {
			return 0, fmt.Errorf("reading placement ticket %s: %w", k, err)
		}
		var t ticketValue
		if len(raw) > 0 && json.Unmarshal(raw, &t) == nil && slices.Contains(models, t.ModelName) {
			count++
		}
	}
	return count, nil
}

// ServerStateRepublisher runs once at operator start, when the bus is ready, and publishes
// every ModelServer's state, so an entry the previous operator left marked draining (it
// ended mid-switch) is overwritten at once and not at the next reconcile.
type ServerStateRepublisher struct {
	Reader  client.Reader
	Servers *ModelServerReconciler
	// Ready is nil when the bus is ready, else why not; nil means ready.
	Ready func() error
	// PollInterval paces the wait for the bus; zero is one second. It never ends the wait.
	PollInterval time.Duration
}

// NeedLeaderElection keeps the publish to the leader.
func (p *ServerStateRepublisher) NeedLeaderElection() bool { return true }

// Start waits for the bus by state, publishes once, and returns; the manager keeps running.
func (p *ServerStateRepublisher) Start(ctx context.Context) error {
	every := p.PollInterval
	if every <= 0 {
		every = time.Second
	}
	for p.Ready != nil && p.Ready() != nil {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
	if err := p.Republish(ctx); err != nil {
		logf.FromContext(ctx).Error(err, "Could not republish the server states at start")
	}
	return nil
}

// Republish probes (best effort) and publishes the state of every ModelServer.
func (p *ServerStateRepublisher) Republish(ctx context.Context) error {
	list := &aiv1alpha1.ModelServerList{}
	if err := p.Reader.List(ctx, list); err != nil {
		return fmt.Errorf("listing ModelServers: %w", err)
	}
	var errs []error
	for i := range list.Items {
		server := &list.Items[i]
		if _, err := p.Servers.probeSnapshot(ctx, server, p.Servers.refreshClient()); err != nil {
			logf.FromContext(ctx).V(1).Info("could not probe the server at start; publishing its last snapshot", "server", server.Name, "err", err)
		}
		if err := p.Servers.publishState(ctx, server); err != nil {
			errs = append(errs, fmt.Errorf("server %s: %w", server.Name, err))
		}
	}
	return errors.Join(errs...)
}
