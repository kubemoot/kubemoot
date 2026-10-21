/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package mesh holds the contract between the operator (the Moot Mesh control plane) and an
// agent pod's sidecar (the data plane): the contract.yaml the operator writes for each agent
// and the sidecar reads at start, the names both sides must agree on, and the NATS permissions
// the sidecar needs. The operator and the sidecar import this one definition, so the file's
// shape has a single source.
//
// The package holds data types, their validation, and pure functions over them. It imports no
// Kubernetes library, so the sidecar binary does not carry one.
package mesh

import (
	"bytes"
	"fmt"

	"sigs.k8s.io/yaml"
)

// ContractVersion is the contract.yaml version this package reads and writes. It equals the
// major version of the bus contract the sidecar serves, so a sidecar refuses a file written for
// a bus it does not speak. A change that removes a field or changes its meaning is a new
// version.
const ContractVersion = "2"

// Fixed locations inside an agent pod. The operator mounts files here and the sidecar reads
// them, so the paths live in one place.
const (
	// ContractDir holds contract.yaml, mounted from the agent's contract ConfigMap.
	ContractDir = "/etc/kubemoot/contract"
	// ContractFile is the file name inside ContractDir.
	ContractFile = "contract.yaml"
	// PolicyDir holds system.txt, the agent's assembled instructions.
	PolicyDir = "/etc/kubemoot/policy"
	// SystemPromptFile is the file name inside PolicyDir.
	SystemPromptFile = "system.txt"
	// SkillsDir holds the crew's skills, one file per skill.
	SkillsDir = "/app/config/skills"
	// NATSCredentialsDir holds the sidecar's own NATS credentials, mounted only into the sidecar.
	NATSCredentialsDir = "/etc/kubemoot/nats"
)

// Loopback addresses of the three doors, and the ports the sidecar and the harness listen on.
// The harness receives the URLs as environment variables, never the NATS or gateway addresses.
const (
	// ModelDoorAddr is where the sidecar serves the OpenAI-compatible model door.
	ModelDoorAddr = "127.0.0.1:8081"
	// ToolsDoorAddr is where the sidecar serves the MCP tools door.
	ToolsDoorAddr = "127.0.0.1:8082"
	// DefaultHTTPPort is the sidecar's pod-facing port: health, metrics, and chat ingress.
	DefaultHTTPPort = 8080
	// DefaultTurnPort is the port a harness serves the turn on.
	DefaultTurnPort = 9000
)

// NativeTools says what a harness's own built-in tools may do.
type NativeTools string

// The values of Agent.spec.harness.nativeTools.
const (
	// NativeToolsNone turns the harness's own tools off; only the tools door remains.
	NativeToolsNone NativeTools = "none"
	// NativeToolsDeclared allows the built-ins the crew author reviewed and declared.
	NativeToolsDeclared NativeTools = "declared"
	// NativeToolsAll leaves every built-in on: a deliberate crew-author choice.
	NativeToolsAll NativeTools = "all"
)

// Contract is the whole contract.yaml.
type Contract struct {
	ContractVersion string         `json:"contractVersion"`
	Identity        Identity       `json:"identity"`
	Bus             Bus            `json:"bus"`
	Turn            Turn           `json:"turn"`
	Models          Models         `json:"models"`
	Placement       Placement      `json:"placement"`
	Tools           Tools          `json:"tools"`
	Policy          *Policy        `json:"policy,omitempty"`
	Observability   *Observability `json:"observability,omitempty"`
}

// Identity says who the agent is on the bus. Namespace, crew and agent scope every subject and
// key the sidecar builds.
type Identity struct {
	Namespace     string   `json:"namespace"`
	Crew          string   `json:"crew"`
	Agent         string   `json:"agent"`
	Role          string   `json:"role,omitempty"`
	Channels      []string `json:"channels,omitempty"`
	TriageSummary string   `json:"triageSummary,omitempty"`
	// Harness is the harness's name, shown on the dashboard and carried on every signal.
	Harness string `json:"harness,omitempty"`
}

// Bus says how the sidecar reaches NATS.
type Bus struct {
	NATSURL string `json:"natsURL"`
	// Stream and Consumer name the operator's durable JetStream consumer for this agent. The
	// sidecar binds it by name. With no consumer the sidecar subscribes on core NATS.
	Stream   string `json:"stream,omitempty"`
	Consumer string `json:"consumer,omitempty"`
	// ProgressAckSeconds is how often the sidecar tells JetStream a message is still being
	// worked, so a turn longer than the consumer's AckWait is not delivered again.
	ProgressAckSeconds int `json:"progressAckSeconds,omitempty"`
	// CredentialsDir is where the sidecar finds its own NATS credentials (creds, or user and
	// password). An absent directory means an anonymous connection.
	CredentialsDir string `json:"credentialsDir,omitempty"`
}

// Turn says how the sidecar reaches the harness.
type Turn struct {
	// URL is the harness's A2A root, for example http://127.0.0.1:9000.
	URL string `json:"url"`
	// Concurrency is how many turns may run at once. Above 1 the harness must send the per-turn
	// key on every model and tool call.
	Concurrency int `json:"concurrency"`
	// DeadlineSeconds is the safety net for one turn; the normal path ends on state.
	DeadlineSeconds int `json:"deadlineSeconds"`
}

// Models says which models each alias stands for.
type Models struct {
	// Aliases maps an alias the harness may name (default, triage) to its models.
	Aliases map[string]Alias `json:"aliases"`
}

// Alias is one model alias: the acceptable models, preferred first, and the server the operator
// bound as the floor when the placement service cannot be reached.
type Alias struct {
	Candidates []string  `json:"candidates"`
	Fallback   *Endpoint `json:"fallback,omitempty"`
}

// Endpoint is one model on one server.
type Endpoint struct {
	Server   string `json:"server"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// Placement says where model calls are placed.
type Placement struct {
	// Policy is the placement policy the crew resolved (algorithm, learned, shadow).
	Policy string `json:"policy,omitempty"`
	// ServiceURL is the placement service. Empty means every call uses the alias's fallback.
	ServiceURL string `json:"serviceURL,omitempty"`
}

// Tools says which tools the harness may use and where they come from.
type Tools struct {
	// Gateway is the crew's MCP gateway. Empty means the agent has no tools.
	Gateway  string   `json:"gateway,omitempty"`
	Enabled  []string `json:"enabled,omitempty"`
	Disabled []string `json:"disabled,omitempty"`
	// NativeTools is recorded for the dashboard and the receipt; the harness image applies it.
	NativeTools NativeTools `json:"nativeTools,omitempty"`
}

// Policy points at the files that become the harness's instructions.
type Policy struct {
	SystemPromptFile string `json:"systemPromptFile,omitempty"`
	SkillsDir        string `json:"skillsDir,omitempty"`
}

// Observability says where traces go.
type Observability struct {
	// OTLPEndpoint is an OTLP/HTTP collector, for example http://otel-collector:4318. Empty
	// turns trace export off; trace context still flows through every hop.
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`
}

// Parse reads contract.yaml. Unknown fields are an error: a file the sidecar does not fully
// understand is a mismatch to report, not to run.
func Parse(data []byte) (Contract, error) {
	var c Contract
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return Contract{}, fmt.Errorf("reading contract: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Contract{}, err
	}
	return c, nil
}

// Marshal writes the contract as YAML, after checking it.
func (c Contract) Marshal() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(out), nil
}
