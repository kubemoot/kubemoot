/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Bounds on status.realm. A suite's status lives in etcd with the rest of the
// object, so every list is capped; the full record of a run stays in these
// bounds (about 20 servers of 30 models each, two snapshots).
const (
	MaxRealmServers  = 20
	MaxRealmModels   = 30
	MaxRealmAgents   = 20
	MaxRealmSuites   = 10
	MaxRealmNotes    = 20
	MaxRealmDrift    = 30
	RealmDigestChars = 12
)

// FitnessRealm records what a suite run ran on: the model servers and models,
// the accelerator capability behind them, the versions, the agent pod resources,
// and what else was running. Infrastructure alone moves agentic eval scores by
// several points, so a result without its realm is not comparable to another.
// The operator takes one snapshot when the run starts and one when it ends.
type FitnessRealm struct {
	// Start is the realm when the first trial began.
	// +optional
	Start *RealmSnapshot `json:"start,omitempty"`

	// End is the realm when the run reached a terminal phase.
	// +optional
	End *RealmSnapshot `json:"end,omitempty"`

	// Digests are short hashes of the parts of Start that decide whether two
	// runs are comparable. Two runs with equal digests ran on the same realm;
	// a differing digest names the part that differs. They leave out time,
	// residency, and concurrent activity, which are recorded but never make
	// two runs incomparable on their own.
	// +optional
	Digests *RealmDigests `json:"digests,omitempty"`

	// Drift lists what changed between Start and End during the run (a model
	// replaced, an agent pod rescheduled with other limits, another suite
	// started). A run with drift measured a moving system.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=30
	Drift []string `json:"drift,omitempty"`

	// CopiedFrom is set on a re-judge suite: the answers ran on the source
	// run's realm, so Start and Digests are the source's.
	// +optional
	CopiedFrom *RejudgeSource `json:"copiedFrom,omitempty"`
}

// RealmDigests hashes the comparable parts of a realm, one per part, so a
// reader can say which part differs without a field-by-field diff.
type RealmDigests struct {
	// Servers covers each model server: its name, engine, engine version, and
	// accelerator capability.
	Servers string `json:"servers"`
	// Models covers each model on each server: name, digest, quantization,
	// and context length.
	Models string `json:"models"`
	// Versions covers the operator version, the crew chart version, and the
	// images of the crew's pods.
	Versions string `json:"versions"`
	// Resources covers the requests and limits of the crew's pods.
	Resources string `json:"resources"`
}

// RealmSnapshot is the realm at one moment.
type RealmSnapshot struct {
	// ObservedAt is when the snapshot was taken.
	ObservedAt metav1.Time `json:"observedAt"`

	// OperatorVersion is the operator's build version; "dev" for an unversioned build.
	// +optional
	OperatorVersion string `json:"operatorVersion,omitempty"`

	// CrewVersion is the crew chart version from the crew's
	// kubemoot.ai/crew-version label; absent for a hand-applied crew.
	// +optional
	CrewVersion string `json:"crewVersion,omitempty"`

	// ModelServers lists the model servers the crew's models can run on.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	ModelServers []RealmModelServer `json:"modelServers,omitempty"`

	// Agents lists the distinct containers of the crew's agent pods with their
	// image and resources, one row per distinct combination.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Agents []RealmAgentContainer `json:"agents,omitempty"`

	// ResourceNotes flags agent pod requests and limits that make a throttled
	// agent likely: a request that is unset, or a limit equal to the request.
	// Guidance is a guaranteed allocation plus a higher hard limit.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	ResourceNotes []string `json:"resourceNotes,omitempty"`

	// ConcurrentSuites lists other CrewFitnessSuites in a running phase when
	// the snapshot was taken, as namespace/name. A measurement expects none.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10
	ConcurrentSuites []string `json:"concurrentSuites,omitempty"`

	// ConcurrentRuns counts standalone CrewFitness runs in flight, outside this suite.
	// +optional
	ConcurrentRuns int32 `json:"concurrentRuns,omitempty"`
}

// RealmModelServer is one model server and what it ran on.
type RealmModelServer struct {
	// Name and Namespace identify the ModelServer.
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Engine is the engine type (ollama, vllm).
	// +optional
	Engine string `json:"engine,omitempty"`

	// EngineVersion is the version the engine reported.
	// +optional
	EngineVersion string `json:"engineVersion,omitempty"`

	// Ready is whether the server was ready.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Accelerator is the capability behind the server. It is described by what
	// it can do (memory, parallelism, context), never by vendor or device name.
	// +optional
	Accelerator *RealmAccelerator `json:"accelerator,omitempty"`

	// Models lists the models on the server, with which were resident.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=30
	Models []RealmModel `json:"models,omitempty"`
}

// RealmAccelerator is the accelerator capability behind a model server.
type RealmAccelerator struct {
	// MemoryMiB is the accelerator memory the server can use, in MiB. Zero when
	// the server reports none (a CPU-only or hosted engine).
	// +optional
	MemoryMiB int64 `json:"memoryMiB,omitempty"`

	// MemoryUsedMiB is the memory in use when the snapshot was taken.
	// +optional
	MemoryUsedMiB int64 `json:"memoryUsedMiB,omitempty"`

	// MaxParallel is how many requests the engine serves at once.
	// +optional
	MaxParallel int32 `json:"maxParallel,omitempty"`

	// ContextLength is the context window, in tokens, the engine gives a request.
	// +optional
	ContextLength int32 `json:"contextLength,omitempty"`
}

// RealmModel is one model on a server.
type RealmModel struct {
	// Name is the model identifier on the server.
	Name string `json:"name"`

	// Digest is the content digest the engine reported; absent when it reported none.
	// +optional
	Digest string `json:"digest,omitempty"`

	// Quantization of the weights, as the engine reported it.
	// +optional
	Quantization string `json:"quantization,omitempty"`

	// ContextLength is the model's context window in tokens.
	// +optional
	ContextLength int32 `json:"contextLength,omitempty"`

	// Resident is true when the model was loaded on the accelerator at the
	// moment of the snapshot. At Start it tells a cold run from a warm one.
	// +optional
	Resident bool `json:"resident,omitempty"`
}

// RealmAgentContainer is one distinct container shape across the crew's pods.
type RealmAgentContainer struct {
	// Container is the container name.
	Container string `json:"container"`
	// Image is the image reference the container ran.
	// +optional
	Image string `json:"image,omitempty"`
	// Pods is how many of the crew's pods run this combination.
	Pods int32 `json:"pods"`
	// CPURequest, CPULimit, MemoryRequest and MemoryLimit are the container's
	// resources as quantities; empty when unset.
	// +optional
	CPURequest string `json:"cpuRequest,omitempty"`
	// +optional
	CPULimit string `json:"cpuLimit,omitempty"`
	// +optional
	MemoryRequest string `json:"memoryRequest,omitempty"`
	// +optional
	MemoryLimit string `json:"memoryLimit,omitempty"`
}
