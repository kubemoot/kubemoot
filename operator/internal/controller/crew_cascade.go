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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

// adoptCrewAgents adds the Crew as a non-controlling owner of each member Agent
// so Kubernetes garbage collection removes the Agents when the Crew goes away.
// The reference is additive: it never replaces another owner and does not alter
// any field a Helm release or Flux manifest manages.
func (r *CrewReconciler) adoptCrewAgents(ctx context.Context, crew *kubemootv1alpha1.Crew, agents []kubemootv1alpha1.Agent) {
	log := logf.FromContext(ctx)
	for i := range agents {
		agent := &agents[i]
		if !agent.DeletionTimestamp.IsZero() || hasOwnerUID(agent, crew) {
			continue
		}
		base := agent.DeepCopy()
		if err := controllerutil.SetOwnerReference(crew, agent, r.Scheme); err != nil {
			log.Error(err, "Failed to build owner reference", "agent", agent.Name)
			continue
		}
		if err := r.Patch(ctx, agent, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil && !errors.IsNotFound(err) {
			log.Error(err, "Failed to set Crew owner reference on Agent", "agent", agent.Name)
		}
	}
}

func hasOwnerUID(agent *kubemootv1alpha1.Agent, crew *kubemootv1alpha1.Crew) bool {
	for _, ref := range agent.OwnerReferences {
		if ref.UID == crew.UID {
			return true
		}
	}
	return false
}

// listCrewAgents lists the Agents in the Crew's namespace that carry the Crew's label.
func (r *CrewReconciler) listCrewAgents(ctx context.Context, crew *kubemootv1alpha1.Crew) (kubemootv1alpha1.AgentList, error) {
	var agents kubemootv1alpha1.AgentList
	err := r.List(ctx, &agents, client.InNamespace(crew.Namespace), client.MatchingLabels{labelCrew: crew.Name})
	return agents, err
}

// deleteCrewAgents deletes every Agent in the Crew's namespace that carries the
// Crew's label, which covers Agents created before the owner reference existed.
// Agents of other crews and Agents without the label are never listed. A List or
// Delete failure is returned so the caller keeps the finalizer and retries.
func (r *CrewReconciler) deleteCrewAgents(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	agents, err := r.listCrewAgents(ctx, crew)
	if err != nil {
		return err
	}
	var errs []error
	for i := range agents.Items {
		if err := r.Delete(ctx, &agents.Items[i]); err != nil && !errors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete agent %s: %w", agents.Items[i].Name, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

// crewDiscussionBus is the slice of *nats.Publisher the Crew finalizer uses to
// remove a deleted crew's discussion state (so tests can substitute a fake).
type crewDiscussionBus interface {
	PurgeSubject(stream, filter string) error
	DeleteKVKeys(bucket string, keys []string) error
}

// discussionBus returns the bus for discussion cleanup, or nil when none is configured.
func (r *CrewReconciler) discussionBus() crewDiscussionBus {
	if r.bus != nil {
		return r.bus
	}
	if r.NATSPublisher == nil {
		return nil // a nil *Publisher must not become a non-nil interface
	}
	return r.NATSPublisher
}

// crewAgentNames returns the names of the Agents that carry the Crew's label. A
// List failure yields none; deleteCrewAgents reports it and the finalizer retries.
func (r *CrewReconciler) crewAgentNames(ctx context.Context, crew *kubemootv1alpha1.Crew) []string {
	agents, err := r.listCrewAgents(ctx, crew)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(agents.Items))
	for i := range agents.Items {
		names = append(names, agents.Items[i].Name)
	}
	return names
}

// purgeCrewDiscussions removes the deleted crew's discussion state from NATS: every
// message under kubemoot.discuss.<namespace>.<crew>.> in the discussion stream, and
// the liveness heartbeat (<namespace>.<agent>) of each of its Agents. Both come from
// the same namespace and crew name builders the sidecars use, so a same-named crew
// in another namespace and other crews are never matched. Any failure is returned
// so the caller keeps the finalizer and retries.
func (r *CrewReconciler) purgeCrewDiscussions(ctx context.Context, crew *kubemootv1alpha1.Crew, agentNames []string) error {
	b := r.discussionBus()
	if b == nil {
		return nil
	}
	id := mesh.Identity{Namespace: crew.Namespace, Crew: crew.Name}
	if err := b.PurgeSubject(bus.DiscussStream, id.DiscussWildcard()); err != nil {
		return err
	}
	keys := make([]string, 0, len(agentNames))
	for _, name := range agentNames {
		keys = append(keys, mesh.Identity{Namespace: crew.Namespace, Agent: name}.HeartbeatKey())
	}
	if err := b.DeleteKVKeys(mesh.AgentStateBucket, keys); err != nil {
		return err
	}
	logf.FromContext(ctx).Info("Purged crew discussion state on deletion", "crew", crew.Name, "agents", len(keys))
	return nil
}

// recordDeletionFailure puts a failed deletion cleanup on the Crew as a Warning event.
func (r *CrewReconciler) recordDeletionFailure(crew *kubemootv1alpha1.Crew, err error) {
	if r.Recorder != nil {
		r.Recorder.Eventf(crew, nil, corev1.EventTypeWarning, "DiscussionPurgeFailed", "Delete", "Removing discussion state from NATS failed, will retry: %v", err)
	}
}
