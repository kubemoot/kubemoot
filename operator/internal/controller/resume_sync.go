/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// resume_sync.go restores the embedding-based resume pipeline that drives
// coordinator subcommittee (triage) selection. It was shipped 2026-03-23 and
// demolished as collateral in 47e09c0 (the v2 agent-scheduling clean slate);
// the agent-runtime side (ResumeSearchClient, vector pre-filter BEFORE
// broadcast) survived intact and only waits on the operator to (a) publish each
// crew's agent resumes and (b) point the coordinator at the resume query
// service. See tasks/notes/Restore Resume-Based Subcommittee Selection.md.
//
// Flow (deploy-time, hash-gated — NOT per-discussion):
//
//	coordinator Agent reconcile
//	  → compile a resume per crew tooler (from the Agent specs)
//	  → write the resume set to NATS KV bucket kubemoot_crew_resumes, key=<ns>.<crew>
//	      (and delete the unscoped key <crew> it replaces)
//	  → create/update a per-crew RAGSource crew-<crew>-resumes in the crew namespace
//	      (collection crew_<ns>_<crew>_resumes), copying the cluster's existing
//	      vectorStore + embeddingModel config; the RAGSource controller runs the
//	      indexer (embeds) and stands up the query service automatically
//	  → buildEnvVars injects KUBEMOOT_RESUME_SEARCH_ENDPOINT on the coordinator
//
// Change detection anchors on the RAGSource's NatsKV.ContentHash: re-embedding
// happens only when an agent's resume changes, so resumes are ready before the
// first discussion and never perturb run timing. Per-crew collections keep
// crews isolated; the RAGSource is owner-referenced to the coordinator so it is
// garbage-collected with the crew.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	"github.com/javajon/kubemoot/operator/internal/crewscope"
)

const (
	// crewLabelKey is declared in crew_controller.go (= labelCrew).
	triageSummaryAnno = "kubemoot.ai/triage-summary"
	resumeKVBucket    = "kubemoot_crew_resumes"
	resumeQueryPort   = 8000
	roleCoordinator   = "coordinator"
	roleToolerResume  = "tooler"
)

// syncCrewResumes compiles the crew's agent resumes and publishes them for
// embedding-based triage. Only acts on the coordinator agent (it owns the crew
// view). Non-fatal: every failure logs and returns so a resume hiccup never
// blocks the coordinator from coming up — the agent-runtime degrades to keyword
// self-selection when the resume search is unavailable.
func (r *AgentReconciler) syncCrewResumes(ctx context.Context, coordinator *kubemootv1alpha1.Agent) {
	log := logf.FromContext(ctx)
	if coordinator.Spec.DiscussRole != roleCoordinator {
		return
	}
	scope, err := coordinatorScope(coordinator)
	if err != nil {
		log.Info("Crew resumes not synced: the crew cannot be scoped", "error", err.Error())
		return
	}
	crewName := scope.Crew

	resumes := r.compileCrewResumes(ctx, coordinator, crewName)
	if len(resumes) == 0 {
		return
	}
	skills := r.listCrewSkills(ctx, coordinator, crewName)
	newHash := CombinedResumeHash(resumes, skills)

	ragSourceName := resumeRAGSourceName(crewName)
	if r.resumesUnchanged(ctx, coordinator.Namespace, ragSourceName, newHash) {
		log.V(1).Info("Crew resumes unchanged, skipping re-embed", "crew", crewName)
		return
	}

	if !r.writeResumesToNATS(ctx, scope, resumes, skills) {
		return
	}

	vectorStore, embeddingModelRef, ok := r.resumeVectorStore(ctx, coordinator, crewName)
	if !ok {
		return
	}
	desiredSpec := buildResumeRAGSourceSpec(scope, newHash, vectorStore, embeddingModelRef)
	if r.createOrUpdateResumeRAGSource(ctx, coordinator, ragSourceName, crewName, desiredSpec) {
		log.Info("Resume RAGSource synced", "crew", crewName, "agents", len(resumes), "hash", newHash[:12])
	}
}

// coordinatorScope is the coordinator's namespace and crew: the crew label, or
// the namespace when the label is absent.
func coordinatorScope(coordinator *kubemootv1alpha1.Agent) (crewscope.Scope, error) {
	crewName := coordinator.Labels[crewLabelKey]
	if crewName == "" {
		crewName = coordinator.Namespace
	}
	return crewscope.New(coordinator.Namespace, crewName)
}

// resumeRAGSourceName names the crew's resume RAGSource, a namespaced object
// in the crew namespace (its query service is <name>-query).
func resumeRAGSourceName(crewName string) string {
	return fmt.Sprintf("crew-%s-resumes", crewName)
}

// resumesUnchanged reports whether the resume RAGSource already carries hash.
func (r *AgentReconciler) resumesUnchanged(ctx context.Context, namespace, ragSourceName, hash string) bool {
	existing := &kubemootv1alpha1.RAGSource{}
	if err := r.Get(ctx, types.NamespacedName{Name: ragSourceName, Namespace: namespace}, existing); err != nil {
		return false
	}
	return existing.Spec.Source.NatsKV != nil && existing.Spec.Source.NatsKV.ContentHash == hash
}

// resumeVectorStore finds the vectorStore and embedding model the resume
// RAGSource copies and makes sure the vectorStore secret is present in the crew
// namespace. It reports false when the RAGSource cannot be created yet.
func (r *AgentReconciler) resumeVectorStore(ctx context.Context, coordinator *kubemootv1alpha1.Agent, crewName string) (*kubemootv1alpha1.VectorStoreConfig, string, bool) {
	log := logf.FromContext(ctx)
	vectorStore, embeddingModelRef, sourceNamespace := r.discoverRAGSourceDefaults(ctx, coordinator.Namespace)
	if vectorStore == nil {
		// Surfaced at Info (not debug): the resume pipeline cannot engage without a
		// vectorStore to copy, so this is an actionable "feature off" signal, not noise.
		log.Info("Resume RAGSource not created: no existing RAGSource found to copy vectorStore/embeddingModel config from", "crew", crewName)
		return nil, "", false
	}
	if vectorStore.SecretRef == "" {
		return vectorStore, embeddingModelRef, true
	}
	// Replicate the vectorStore secret into the crew namespace from the source
	// RAGSource's namespace if it isn't already here.
	if sourceNamespace != coordinator.Namespace && !secretExists(ctx, r.Client, vectorStore.SecretRef, coordinator.Namespace) {
		replicateSecretFrom(ctx, r.Client, vectorStore.SecretRef, sourceNamespace, coordinator.Namespace)
	}
	// Gate RAGSource creation on the secret actually being present. Otherwise the
	// resume indexer/query pods come up into CreateContainerConfigError and the
	// resume collection never indexes (selection silently falls back to broadcast).
	// A later reconcile — or the namespace controller's own secret replication —
	// lands the secret; we create the RAGSource on that retry.
	if !secretExists(ctx, r.Client, vectorStore.SecretRef, coordinator.Namespace) {
		log.Info("Resume RAGSource deferred: vectorStore secret not yet present in crew namespace; will retry",
			"crew", crewName, "secret", vectorStore.SecretRef, "namespace", coordinator.Namespace)
		return nil, "", false
	}
	return vectorStore, embeddingModelRef, true
}

// compileCrewResumes lists the crew's tooler agents and builds one resume
// each (the coordinator itself is excluded — it is the selector, not a
// candidate). Sorted by name so the hash is deterministic.
func (r *AgentReconciler) compileCrewResumes(ctx context.Context, coordinator *kubemootv1alpha1.Agent, crewName string) []AgentResume {
	log := logf.FromContext(ctx)
	agentList := &kubemootv1alpha1.AgentList{}
	listOpts := []client.ListOption{client.InNamespace(coordinator.Namespace)}
	if crewName != "" && crewName != coordinator.Namespace {
		listOpts = append(listOpts, client.MatchingLabels{crewLabelKey: crewName})
	}
	if err := r.List(ctx, agentList, listOpts...); err != nil {
		log.Info("Failed to list agents for crew resumes", "error", err)
		return nil
	}

	var resumes []AgentResume
	for i := range agentList.Items {
		a := &agentList.Items[i]
		if a.Name == coordinator.Name || a.Spec.DiscussRole == roleCoordinator {
			continue
		}
		resumes = append(resumes, r.buildAgentResume(ctx, a))
	}
	sort.Slice(resumes, func(i, j int) bool { return resumes[i].Name < resumes[j].Name })
	return resumes
}

// buildAgentResume projects an Agent spec into the resume the indexer embeds and
// the triage selector reads. The concise declarative fields (description,
// keywords, tools, channels, role) plus the agent's FULL assembled system prompt —
// the richest statement of what the agent does — which the indexer folds into the
// embedding for sharper top-K ranking. The prompt rides the embedding only: the
// resume query returns agent NAMES, so it never reaches a triage LLM call
// (embed-rich, triage-lean). A prompt-assembly error is non-fatal — the resume
// still embeds on its declarative fields.
func (r *AgentReconciler) buildAgentResume(ctx context.Context, agent *kubemootv1alpha1.Agent) AgentResume {
	role := agent.Spec.DiscussRole
	if role == "" {
		role = roleToolerResume
	}
	summary := agent.Spec.TriageSummary
	if summary == "" && agent.Annotations != nil {
		summary = agent.Annotations[triageSummaryAnno]
	}
	prompt, err := r.assembleSystemPrompt(ctx, agent)
	if err != nil {
		logf.FromContext(ctx).V(1).Info("resume: prompt assembly failed, embedding declarative fields only",
			"agent", agent.Name, "error", err.Error())
		prompt = ""
	}
	return AgentResume{
		Name:        agent.Name,
		Description: agent.Spec.Description,
		Keywords:    agent.Spec.DiscussKeywords,
		Tools:       agent.Spec.EnabledTools,
		Channels:    agent.Spec.DiscussChannels,
		Role:        role,
		Summary:     summary,
		Prompt:      prompt,
	}
}

// writeResumesToNATS stores the resume set as JSON in the shared NATS KV bucket,
// keyed <ns>.<crew>, and deletes the unscoped <crew> key it replaces. The indexer
// reads from here (RAGSource source type nats-kv).
// Returns true when written (or when NATS is not configured - a no-op build).
// When skills is empty the payload is byte-identical to the previous agent-only format.
func (r *AgentReconciler) writeResumesToNATS(ctx context.Context, scope crewscope.Scope, resumes []AgentResume, skills []SkillResume) bool {
	log := logf.FromContext(ctx)
	if r.NATSPublisher == nil {
		return true
	}
	resumeJSON, err := marshalResumePayload(resumes, skills)
	if err != nil {
		log.Error(err, "Failed to marshal resumes for NATS KV")
		return false
	}
	if err := r.NATSPublisher.PutKVValue(resumeKVBucket, scope.ResumeKey(), resumeJSON); err != nil {
		log.Error(err, "Failed to write resumes to NATS KV", "namespace", scope.Namespace, "crew", scope.Crew)
		return false
	}
	_ = r.NATSPublisher.DeleteKVKey(resumeKVBucket, crewscope.LegacyResumeKey(scope.Crew))
	log.Info("Wrote crew resumes to NATS KV", "namespace", scope.Namespace, "crew", scope.Crew,
		"agents", len(resumes), "skills", len(skills))
	return true
}

// marshalResumePayload serializes agent resumes and (optionally) skill resumes
// into the NATS KV wire format. When skills is empty the output is IDENTICAL to
// json.Marshal(agents) so existing crews with no Skills see no wire-format change.
func marshalResumePayload(agents []AgentResume, skills []SkillResume) ([]byte, error) {
	if len(skills) == 0 {
		return json.Marshal(agents)
	}
	msgs := make([]json.RawMessage, 0, len(agents)+len(skills))
	for _, a := range agents {
		b, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, b)
	}
	for _, s := range skills {
		b, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, b)
	}
	return json.Marshal(msgs)
}

// listCrewSkills returns a sorted SkillResume slice for the crew. Non-fatal:
// returns nil on any error so a missing Skill CRD never blocks resume sync.
func (r *AgentReconciler) listCrewSkills(ctx context.Context, coordinator *kubemootv1alpha1.Agent, crewName string) []SkillResume {
	log := logf.FromContext(ctx)
	skillList := &kubemootv1alpha1.SkillList{}
	listOpts := []client.ListOption{client.InNamespace(coordinator.Namespace)}
	if crewName != "" && crewName != coordinator.Namespace {
		listOpts = append(listOpts, client.MatchingLabels{crewLabelKey: crewName})
	}
	if err := r.List(ctx, skillList, listOpts...); err != nil {
		log.V(1).Info("listCrewSkills: list failed (Skill CRD may not be installed)", "error", err)
		return nil
	}
	skills := make([]SkillResume, 0, len(skillList.Items))
	for i := range skillList.Items {
		s := &skillList.Items[i]
		if !s.DeletionTimestamp.IsZero() {
			continue
		}
		skills = append(skills, SkillResume{
			Name:        s.Name,
			Description: s.Spec.Description,
			Kind:        "skill",
			Order:       s.Spec.Order,
		})
	}
	if len(skills) == 0 {
		return nil
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Order != skills[j].Order {
			return skills[i].Order < skills[j].Order
		}
		return skills[i].Name < skills[j].Name
	})
	return skills
}

// discoverRAGSourceDefaults copies vectorStore + embeddingModel config from an
// existing non-resume RAGSource so the resume RAGSource embeds with the same
// store the cluster already uses. Tries the coordinator's namespace first, then
// all namespaces (crew namespaces often have no RAGSources of their own, but the
// pilot namespace does). Returns the config plus the namespace it came from (for
// secret replication).
func (r *AgentReconciler) discoverRAGSourceDefaults(ctx context.Context, namespace string) (*kubemootv1alpha1.VectorStoreConfig, string, string) {
	for _, opts := range [][]client.ListOption{
		{client.InNamespace(namespace)},
		{},
	} {
		ragList := &kubemootv1alpha1.RAGSourceList{}
		if err := r.List(ctx, ragList, opts...); err != nil {
			continue
		}
		for i := range ragList.Items {
			rag := &ragList.Items[i]
			if rag.Spec.Source.Type == kubemootv1alpha1.RAGSourceTypeNatsKV {
				continue // skip resume sources themselves
			}
			if rag.Spec.VectorStore.Endpoint != "" && rag.Spec.EmbeddingModelRef != "" {
				return rag.Spec.VectorStore.DeepCopy(), rag.Spec.EmbeddingModelRef, rag.Namespace
			}
		}
	}
	return nil, "", ""
}

// buildResumeRAGSourceSpec builds the per-crew resume RAGSource spec: a nats-kv
// source (the bucket/key written above, carrying the content hash) feeding the
// crew's namespaced collection via the discovered vectorStore + embeddingModel.
func buildResumeRAGSourceSpec(scope crewscope.Scope, contentHash string, vectorStore *kubemootv1alpha1.VectorStoreConfig,
	embeddingModelRef string) kubemootv1alpha1.RAGSourceSpec {
	return kubemootv1alpha1.RAGSourceSpec{
		Source: kubemootv1alpha1.SourceConfig{
			Type: kubemootv1alpha1.RAGSourceTypeNatsKV,
			NatsKV: &kubemootv1alpha1.NatsKVSource{
				Bucket:      resumeKVBucket,
				Key:         scope.ResumeKey(),
				ContentHash: contentHash,
			},
		},
		VectorStore: kubemootv1alpha1.VectorStoreConfig{
			Type:       vectorStore.Type,
			Endpoint:   vectorStore.Endpoint,
			SecretRef:  vectorStore.SecretRef,
			Collection: scope.ResumeCollection(),
			Dimensions: vectorStore.Dimensions,
		},
		EmbeddingModelRef: embeddingModelRef,
		// One vector per resume: a resume must stay a single chunk so a top-K
		// search returns K DISTINCT agents (the query dedupes by agent_name).
		// Enriching the resume with the full system prompt made the text long
		// enough to trigger the default chunker (~6 chunks/agent), which collapsed
		// K chunks to far fewer distinct agents and starved triage. A large chunk
		// size keeps each resume atomic; a very long prompt truncates at the embed
		// model's context limit rather than splitting.
		Chunking: &kubemootv1alpha1.ChunkingConfig{ChunkSize: 20000, ChunkOverlap: 0},
	}
}

// createOrUpdateResumeRAGSource creates the resume RAGSource (owner-referenced to
// the coordinator so it cascades on crew teardown) or updates its spec in place.
// Returns true on success.
func (r *AgentReconciler) createOrUpdateResumeRAGSource(ctx context.Context, coordinator *kubemootv1alpha1.Agent,
	ragSourceName, crewName string, desiredSpec kubemootv1alpha1.RAGSourceSpec) bool {
	log := logf.FromContext(ctx)

	ragSource := &kubemootv1alpha1.RAGSource{}
	err := r.Get(ctx, types.NamespacedName{Name: ragSourceName, Namespace: coordinator.Namespace}, ragSource)
	notFound := apierrors.IsNotFound(err)
	if err != nil && !notFound {
		log.Error(err, "Failed to get resume RAGSource", "name", ragSourceName)
		return false
	}

	if notFound {
		ragSource = &kubemootv1alpha1.RAGSource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ragSourceName,
				Namespace: coordinator.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "kubemoot-operator",
					"app.kubernetes.io/component":  "resume-rag",
					crewLabelKey:                   crewName,
				},
			},
			Spec: desiredSpec,
		}
		if err := controllerutil.SetControllerReference(coordinator, ragSource, r.Scheme); err != nil {
			log.Error(err, "Failed to set owner reference on resume RAGSource")
			return false
		}
		log.Info("Creating resume RAGSource", "name", ragSourceName)
		if err := r.Create(ctx, ragSource); err != nil {
			log.Error(err, "Failed to create resume RAGSource", "name", ragSourceName)
			return false
		}
		return true
	}

	ragSource.Spec = desiredSpec
	log.Info("Updating resume RAGSource", "name", ragSourceName)
	if err := r.Update(ctx, ragSource); err != nil {
		log.Error(err, "Failed to update resume RAGSource", "name", ragSourceName)
		return false
	}
	return true
}
