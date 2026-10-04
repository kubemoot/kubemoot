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

// Kubernetes well-known labels
const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelComponent = "app.kubernetes.io/component"
	labelName      = "app.kubernetes.io/name"
	labelInstance  = "app.kubernetes.io/instance"
	labelVersion   = "app.kubernetes.io/version"
	managedByValue = "kubemoot-operator"
)

// kubemoot.ai annotation and label keys
const (
	annoAutoProvisioned = "kubemoot.ai/auto-provisioned"
	annoDiscussRole     = "kubemoot.ai/discuss-role"
	annoResumeHash      = "kubemoot.ai/resume-hash"
	annoDynamic         = "kubemoot.ai/dynamic"
	annoGateway         = "kubemoot.ai/gateway"
	annoOnboarded       = "kubemoot.ai/onboarded"
	annoDomain          = "kubemoot.ai/domain"
	annoRole            = "kubemoot.ai/role"
	labelAgent          = "kubemoot.ai/agent"
	labelCrew           = "kubemoot.ai/crew"
	// crewVersionLabel carries the crew Helm chart version, stamped by the crew
	// chart's labels helper. Provenance: which crew VERSION produced a thread/run.
	crewVersionLabel = "kubemoot.ai/crew-version"
	labelRAGSource   = "kubemoot.ai/ragsource"
	// labelFitnessHarness marks the operator's own ephemeral fitness-runner
	// (cf-run-*) Job pods so crews can exclude the test harness from
	// "what is failing in the cluster" assessments. Value is always "true".
	labelFitnessHarness = "kubemoot.ai/fitness-harness"
)

// Deployment provenance a CrewForge deploy stamps on the Crew CR.
const (
	annoCrewForgeSource     = "crewforge.kubemoot.ai/source"
	annoCrewForgeOwner      = "crewforge.kubemoot.ai/owner"
	annoCrewForgeRevision   = "crewforge.kubemoot.ai/revision"
	annoCrewForgeChannel    = "crewforge.kubemoot.ai/channel"
	annoCrewForgeDeployedAt = "crewforge.kubemoot.ai/deployed-at"

	// annoNamespaceCrews is the namespace annotation holding the deployment
	// provenance of every Crew in the namespace, as a JSON object keyed by crew name.
	annoNamespaceCrews = "kubemoot.ai/crews"

	// maxCrewRevisions caps Crew.Status.Revisions.
	maxCrewRevisions = 10
)

// ConfigMap key names used across controllers
const (
	keySystemTxt = "system.txt"
)

// Connection string prefixes
const (
	jdbcPostgresPrefix = "jdbc:postgresql://"
)

// Component names
const (
	componentAgent         = "agent"
	componentQueryService  = "query-service"
	componentFitnessRunner = "fitness-runner"
	componentRAGIndexer    = "rag-indexer"
	componentMCPGateway    = "mcp-gateway"
	componentMCPServer     = "mcp-server"
)

// MCP bridge paths and formats
const (
	bridgePipeDir  = "/pipes"
	svcEndpointFmt = "http://%s.%s:%d"
)

// Boolean values as they are written into env vars, labels, and annotations.
const (
	valueTrue  = "true"
	valueFalse = "false"
)

// Status phases and states shared by several resources.
const (
	phaseReady = "Ready"
	// conditionTypeReady is the type of the Ready status condition.
	conditionTypeReady = "Ready"
	phaseDeploying     = "Deploying"
	phaseIndexing      = "Indexing"
	stateAvailable     = "Available"
)

// Workload wiring shared by the Deployments and Services the operator builds.
const (
	portNameHTTP      = "http"
	envPort           = "PORT"
	envServerPort     = "SERVER_PORT"
	healthPath        = "/health"
	secretKeyUsername = "username"
	secretKeyPassword = "password"
	// skillsVolumeName names the volume that mounts the skills ConfigMap.
	skillsVolumeName = "skills"
	// skillsComponent is the component label of the skills ConfigMap.
	skillsComponent = "skills"
	// defaultOperatorNamespace is where the operator runs when OPERATOR_NAMESPACE is unset.
	defaultOperatorNamespace = "kubemoot"
	// partOfValue is the app.kubernetes.io/part-of label value.
	partOfValue = "kubemoot"
)

// Keys of the JSON documents and unstructured objects the operator writes.
const (
	jsonKeyName    = "name"
	jsonKeyType    = "type"
	jsonKeyMessage = "message"
	jsonKeyPhase   = "phase"
	jsonKeyServer  = "server"
	jsonKeySuccess = "success"
)

// Types of the events in a discussion transcript.
const (
	eventTypeFinding   = "finding"
	eventTypeSynthesis = "synthesis"
	eventTypeDone      = "done"
)
