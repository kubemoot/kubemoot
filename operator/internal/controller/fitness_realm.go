/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_realm.go records the realm a suite run ran on: the model servers and
// models, the accelerator capability behind them, the versions, the crew's pod
// resources, and what else was running, once when the run starts and once when it
// ends. Infrastructure alone moves agentic eval scores by several points, so a
// result is only comparable to another that ran on the same realm. The comparison
// rules live in pkg/fitnessrealm.
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/fitnessrealm"
)

// realmInputs is what a snapshot is built from, so the build is a pure function.
type realmInputs struct {
	suite           *kubemootv1alpha1.CrewFitnessSuite
	operatorVersion string
	crewVersion     string
	policies        []kubemootv1alpha1.CrewSchedulingPolicy
	servers         []kubemootv1alpha1.ModelServer
	models          []kubemootv1alpha1.Model
	pods            []corev1.Pod
	suites          []kubemootv1alpha1.CrewFitnessSuite
	runs            []kubemootv1alpha1.CrewFitness
	now             metav1.Time
}

// buildRealmSnapshot assembles the snapshot; every list is sorted and capped so the
// result is stable and bounded.
func buildRealmSnapshot(in realmInputs) *kubemootv1alpha1.RealmSnapshot {
	snap := &kubemootv1alpha1.RealmSnapshot{
		ObservedAt:      in.now,
		OperatorVersion: in.operatorVersion,
		CrewVersion:     in.crewVersion,
		TrialIsolation:  effectiveIsolation(in.suite),
		ModelServers:    realmServers(scopeServers(in.servers, in.models, in.policies, in.suite.Spec.CrewRef)),
		Agents:          realmAgents(in.pods, in.suite.Spec.CrewRef),
	}
	snap.ResourceNotes = capStrings(fitnessrealm.ResourceNotes(snap.Agents), kubemootv1alpha1.MaxRealmNotes)
	snap.ConcurrentSuites, snap.ConcurrentRuns = realmConcurrency(in.suite, in.suites, in.runs)
	return snap
}

// effectiveIsolation is how the suite's trials were isolated.
func effectiveIsolation(suite *kubemootv1alpha1.CrewFitnessSuite) string {
	switch {
	case !kubemootv1alpha1.BoolOrTrue(suite.Spec.PurgeMemory):
		return "None"
	case trialIsolation(suite) == kubemootv1alpha1.FitnessTrialIsolationPerSuite:
		return string(kubemootv1alpha1.FitnessTrialIsolationPerSuite)
	}
	return string(kubemootv1alpha1.FitnessTrialIsolationPerTrial)
}

// scopeServers narrows the realm to what the crew can use: the models that a rule
// of a scheduling policy for the crew can select, and the servers hosting them. With
// no policy for the crew, or when no model matches a rule, the realm is cluster-wide.
func scopeServers(servers []kubemootv1alpha1.ModelServer, models []kubemootv1alpha1.Model,
	policies []kubemootv1alpha1.CrewSchedulingPolicy, crew string) ([]kubemootv1alpha1.ModelServer, []kubemootv1alpha1.Model) {
	var rules []*kubemootv1alpha1.SchedulingRule
	for i := range policies {
		if policies[i].Spec.CrewRef != crew {
			continue
		}
		for j := range policies[i].Spec.Rules {
			rules = append(rules, &policies[i].Spec.Rules[j])
		}
	}
	if len(rules) == 0 {
		return servers, models
	}
	var scopedModels []kubemootv1alpha1.Model
	hosts := map[string]bool{}
	for _, m := range models {
		if selectableBy(rules, m.Labels) {
			scopedModels = append(scopedModels, m)
			hosts[m.Namespace+"/"+m.Spec.ServerRef] = true
		}
	}
	if len(scopedModels) == 0 {
		return servers, models
	}
	var scopedServers []kubemootv1alpha1.ModelServer
	for _, s := range servers {
		if hosts[s.Namespace+"/"+s.Name] {
			scopedServers = append(scopedServers, s)
		}
	}
	return scopedServers, scopedModels
}

// selectableBy reports whether any rule admits a model with these labels. A rule
// with a constraint that is not valid is treated as admitting it: over-reporting
// the realm is safer than leaving a model out.
func selectableBy(rules []*kubemootv1alpha1.SchedulingRule, lbls map[string]string) bool {
	for _, rule := range rules {
		if ok, err := requireMatches(rule, lbls); ok || err != nil {
			return true
		}
	}
	return false
}

// realmServers lists the model servers with their accelerator capability and models.
func realmServers(servers []kubemootv1alpha1.ModelServer, models []kubemootv1alpha1.Model) []kubemootv1alpha1.RealmModelServer {
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Namespace+"/"+servers[i].Name < servers[j].Namespace+"/"+servers[j].Name
	})
	out := make([]kubemootv1alpha1.RealmModelServer, 0, min(len(servers), kubemootv1alpha1.MaxRealmServers))
	for i := range servers {
		if len(out) == kubemootv1alpha1.MaxRealmServers {
			break
		}
		out = append(out, realmServer(&servers[i], models))
	}
	return out
}

func realmServer(s *kubemootv1alpha1.ModelServer, models []kubemootv1alpha1.Model) kubemootv1alpha1.RealmModelServer {
	out := kubemootv1alpha1.RealmModelServer{
		Name: s.Name, Namespace: s.Namespace, Engine: string(s.Spec.Type),
		Ready: s.Status.Ready, Accelerator: realmAccelerator(s),
		Models: realmModels(s, models),
	}
	if s.Status.ServerInfo != nil {
		out.EngineVersion = s.Status.ServerInfo.Version
	}
	return out
}

// realmAccelerator describes the capability behind a server: memory, parallelism and
// context. It leaves out the device name, since the realm compares what the
// accelerator can do, not whose it is. Nil when the server reports no capability.
func realmAccelerator(s *kubemootv1alpha1.ModelServer) *kubemootv1alpha1.RealmAccelerator {
	acc := &kubemootv1alpha1.RealmAccelerator{}
	if c := s.Status.Capacity; c != nil {
		acc.MemoryMiB, acc.MemoryUsedMiB = c.VRAMTotalMiB, c.VRAMUsedMiB
		acc.MaxParallel, acc.ContextLength = int32(c.MaxParallel), int32(c.ContextLength)
	}
	if acc.MemoryMiB == 0 && s.Spec.Scheduling != nil {
		acc.MemoryMiB = s.Spec.Scheduling.MemoryMiB
	}
	if *acc == (kubemootv1alpha1.RealmAccelerator{}) {
		return nil
	}
	return acc
}

// realmModels lists the server's enabled models with digest and residency.
func realmModels(s *kubemootv1alpha1.ModelServer, models []kubemootv1alpha1.Model) []kubemootv1alpha1.RealmModel {
	loaded := map[string]bool{}
	if s.Status.Capacity != nil {
		for _, l := range s.Status.Capacity.LoadedModels {
			loaded[l.Name] = true
		}
	}
	var out []kubemootv1alpha1.RealmModel
	for i := range models {
		m := &models[i]
		if m.Namespace != s.Namespace || m.Spec.ServerRef != s.Name || m.Spec.Disabled {
			continue
		}
		out = append(out, realmModel(m, loaded[m.Spec.Model]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > kubemootv1alpha1.MaxRealmModels {
		out = out[:kubemootv1alpha1.MaxRealmModels]
	}
	return out
}

func realmModel(m *kubemootv1alpha1.Model, resident bool) kubemootv1alpha1.RealmModel {
	out := kubemootv1alpha1.RealmModel{
		Name: m.Spec.Model, Quantization: m.Spec.Quantization,
		ContextLength: int32(m.Spec.ContextLength), Resident: resident,
	}
	if info := m.Status.ModelInfo; info != nil {
		out.Digest = info.Digest
		if info.Quantization != "" {
			out.Quantization = info.Quantization
		}
		if info.ContextLength != 0 {
			out.ContextLength = int32(info.ContextLength)
		}
	}
	return out
}

// realmAgents groups the crew's running pods by container shape (name, image, and
// resources) and counts the pods of each. The fitness runner's own pods are left out.
func realmAgents(pods []corev1.Pod, crew string) []kubemootv1alpha1.RealmAgentContainer {
	groups := map[kubemootv1alpha1.RealmAgentContainer]int32{}
	for i := range pods {
		p := &pods[i]
		if p.Labels[labelCrew] != crew || p.Labels[labelFitnessHarness] != "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range p.Spec.Containers {
			groups[containerShape(c)]++
		}
	}
	out := make([]kubemootv1alpha1.RealmAgentContainer, 0, len(groups))
	for shape, n := range groups {
		shape.Pods = n
		out = append(out, shape)
	}
	sort.Slice(out, func(i, j int) bool { return agentKey(out[i]) < agentKey(out[j]) })
	if len(out) > kubemootv1alpha1.MaxRealmAgents {
		out = out[:kubemootv1alpha1.MaxRealmAgents]
	}
	return out
}

// agentKey orders container shapes totally (every field that distinguishes two
// shapes), so the capped list is the same on every run.
func agentKey(a kubemootv1alpha1.RealmAgentContainer) string {
	return strings.Join([]string{a.Container, a.Image, a.CPURequest, a.CPULimit, a.MemoryRequest, a.MemoryLimit}, "\x00")
}

// containerShape is the container's name, image and resources, with Pods unset so it
// can key a count.
func containerShape(c corev1.Container) kubemootv1alpha1.RealmAgentContainer {
	return kubemootv1alpha1.RealmAgentContainer{
		Container:     c.Name,
		Image:         c.Image,
		CPURequest:    quantity(c.Resources.Requests, corev1.ResourceCPU),
		CPULimit:      quantity(c.Resources.Limits, corev1.ResourceCPU),
		MemoryRequest: quantity(c.Resources.Requests, corev1.ResourceMemory),
		MemoryLimit:   quantity(c.Resources.Limits, corev1.ResourceMemory),
	}
}

func quantity(list corev1.ResourceList, name corev1.ResourceName) string {
	if q, ok := list[name]; ok && !q.IsZero() {
		return q.String()
	}
	return ""
}

// realmConcurrency finds what else was running: other suites in a running phase, and
// standalone CrewFitness runs in flight. This suite and its own iterations are left out.
func realmConcurrency(self *kubemootv1alpha1.CrewFitnessSuite, suites []kubemootv1alpha1.CrewFitnessSuite, runs []kubemootv1alpha1.CrewFitness) ([]string, int32) {
	var names []string
	for i := range suites {
		s := &suites[i]
		if s.Namespace == self.Namespace && s.Name == self.Name {
			continue
		}
		if s.Status.Phase == kubemootv1alpha1.CrewFitnessSuitePhaseRunning {
			names = append(names, s.Namespace+"/"+s.Name)
		}
	}
	sort.Strings(names)
	var standalone int32
	for i := range runs {
		if runs[i].Labels[suiteOwnerLabel] == "" && !isFitnessTerminal(runs[i].Status.Phase) {
			standalone++
		}
	}
	return capStrings(names, kubemootv1alpha1.MaxRealmSuites), standalone
}

func capStrings(in []string, limit int) []string {
	if len(in) > limit {
		return in[:limit]
	}
	return in
}

// collectRealm reads the cluster state and builds a snapshot. It reads straight from
// the API server so it sees the state now, and it does not start a cache for pods. Any
// failed read fails the whole snapshot: a realm with a part missing would differ from
// a complete one and raise a false warning.
func (r *CrewFitnessSuiteReconciler) collectRealm(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (*kubemootv1alpha1.RealmSnapshot, error) {
	reader := r.liveReader()
	in := realmInputs{suite: suite, operatorVersion: r.OperatorVersion, now: metav1.Now()}
	var servers kubemootv1alpha1.ModelServerList
	var models kubemootv1alpha1.ModelList
	var pods corev1.PodList
	var suites kubemootv1alpha1.CrewFitnessSuiteList
	var runs kubemootv1alpha1.CrewFitnessList
	var policies kubemootv1alpha1.CrewSchedulingPolicyList
	reads := []struct {
		what string
		list client.ObjectList
		opts []client.ListOption
	}{
		{"model servers", &servers, nil},
		{"models", &models, nil},
		{"pods", &pods, []client.ListOption{client.InNamespace(suite.Namespace), client.MatchingLabels{labelCrew: suite.Spec.CrewRef}}},
		{"fitness suites", &suites, nil},
		{"fitness runs", &runs, nil},
		{"scheduling policies", &policies, []client.ListOption{client.InNamespace(suite.Namespace)}},
	}
	for _, rd := range reads {
		if err := reader.List(ctx, rd.list, rd.opts...); err != nil {
			return nil, fmt.Errorf("reading %s for the realm: %w", rd.what, err)
		}
	}
	in.servers, in.models, in.pods, in.suites, in.runs = servers.Items, models.Items, pods.Items, suites.Items, runs.Items
	in.policies = policies.Items
	crew := &kubemootv1alpha1.Crew{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: suite.Namespace, Name: suite.Spec.CrewRef}, crew); err != nil {
		return nil, fmt.Errorf("reading the crew for the realm: %w", err)
	}
	in.crewVersion = crew.Labels[crewVersionLabel] // a crew without the label is hand-applied: no version
	return buildRealmSnapshot(in), nil
}

// recordRealmStart takes the start snapshot into status.realm. A failed read is
// logged and leaves the realm absent: it never stops the suite from running.
func (r *CrewFitnessSuiteReconciler) recordRealmStart(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) {
	snap, err := r.collectRealm(ctx, suite)
	if err != nil {
		logf.FromContext(ctx).Info("realm: start snapshot not recorded", "suite", suite.Name, "err", err.Error())
		return
	}
	suite.Status.Realm = &kubemootv1alpha1.FitnessRealm{Start: snap, Digests: fitnessrealm.DigestsOf(snap)}
}

// retryRealmStart takes the start snapshot again when the first read failed, for as
// long as no iteration has finished, so a transient API error does not cost the run
// its realm.
func (r *CrewFitnessSuiteReconciler) retryRealmStart(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) {
	if suite.Status.Realm == nil && suite.Status.IterationsCompleted == 0 {
		r.recordRealmStart(ctx, suite)
	}
}

// recordRealmEnd takes the end snapshot into status.realm and lists what drifted
// since the start. It records the end once, and only for a run that has a start:
// an end alone has nothing to compare to and would misstate the run's realm.
func (r *CrewFitnessSuiteReconciler) recordRealmEnd(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) {
	if suite.Status.Realm == nil || suite.Status.Realm.Start == nil || suite.Status.Realm.End != nil {
		return
	}
	snap, err := r.collectRealm(ctx, suite)
	if err != nil {
		logf.FromContext(ctx).Info("realm: end snapshot not recorded", "suite", suite.Name, "err", err.Error())
		return
	}
	suite.Status.Realm.End = snap
	suite.Status.Realm.Drift = fitnessrealm.Drift(suite.Status.Realm.Start, snap, kubemootv1alpha1.MaxRealmDrift)
}

// retryRealmEnd takes the end snapshot again on a terminal reconcile when the first
// read failed, and saves it. A re-judge suite's realm is a copy and is left alone.
func (r *CrewFitnessSuiteReconciler) retryRealmEnd(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) {
	realm := suite.Status.Realm
	if realm == nil || realm.Start == nil || realm.End != nil || realm.CopiedFrom != nil {
		return
	}
	base := suite.DeepCopy()
	if r.recordRealmEnd(ctx, suite); suite.Status.Realm.End == nil {
		return
	}
	if err := r.Status().Patch(ctx, suite, client.MergeFrom(base)); err != nil {
		logf.FromContext(ctx).Info("realm: end snapshot not saved; retried on the next reconcile", "suite", suite.Name, "err", err.Error())
	}
}

// copyRealm gives a re-judge suite the realm of the run whose answers it judges.
func copyRealm(source *kubemootv1alpha1.CrewFitnessSuite, from kubemootv1alpha1.RejudgeSource) *kubemootv1alpha1.FitnessRealm {
	if source == nil || source.Status.Realm == nil {
		return nil
	}
	out := source.Status.Realm.DeepCopy()
	out.CopiedFrom = &from
	return out
}
