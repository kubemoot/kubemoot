DEFINE DOMAIN kubemoot
DESCRIPTION Kubernetes operator for multi-agent AI consensus discussions. Inherits ecosystem rules from parent .claude/CLAUDE.md.

# ADL (the Architecture Definition Language), applied to agents

DEFINE COMPONENT adl-policy
DESCRIPTION All agent prompts use ADL - no plain English prose in system prompts

ASSERT all agent prompts use ADL keywords for structure
ASSERT keywords: WHEN/THEN, ALWAYS/NEVER, ASSERT, DESCRIPTION, DEFINE DOMAIN/DEFINE COMPONENT, FOREACH/CONTAINED WITHIN
ASSERT prompts are scannable, versionable (kubectl diff), deployable without recompilation

DEFINE COMPONENT prompt-architecture
DESCRIPTION PromptModule composition and ordering

ASSERT all prompt text lives in PromptModules - never inline spec.prompt.system
ALWAYS reference PromptModules via spec.prompt.promptRefs
WHEN defining shared modules THEN use discussion-protocol (order 10), response-style (order 20)
WHEN defining per-agent modules THEN use {agent-name}-system (order 30)
WHEN defining coordinator modules THEN use advisory-prompt (5), coordinator-decision-logic (45), synthesis-prompt (50)
ASSERT AgentPolicy.spec.prompt.system is deprecated - use PromptModules instead
ASSERT Agent.spec.policyRef is deprecated - use inline guardrails, a2a, observability on Agent spec

# Discussion System

DEFINE COMPONENT discussion-protocol
DESCRIPTION Consensus-based multi-agent discussions via NATS JetStream

ASSERT agents never communicate directly - all messages flow through NATS subjects
ASSERT subject pattern: kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>
ASSERT discussions close when all agents have responded, not after N seconds
ASSERT coordinator determines conclusion via signal-based settling, not quorum or timeout
WHEN an agent publishes triaging THEN coordinator sets generous deadline (~120s)
WHEN an agent publishes evaluating THEN coordinator tightens deadline to P90 latency
WHEN all agents have responded or expired AND 2+ seconds quiet THEN coordinator transitions to synthesis
ASSERT see the parent state-driven-design component - never raise a timeout to fix a bug, find the missing state transition

DEFINE COMPONENT layer-terminology
DESCRIPTION The four interaction layers; drop legacy "consultation"

ASSERT the canonical layers are conversation > turn > discussion > thread (defined in docs/concepts/conversations-turns-and-threads.md): a conversation is the user-facing exchange; a turn is one user message + its response; a discussion is the internal multi-agent deliberation a turn triggers; a thread is the NATS thread carrying one discussion
NEVER use the legacy term "consultation" - it is dropped in favor of "conversation" (user-facing) / "discussion" (internal)
NEVER expose internal layer words (discussion, thread) or signal terms (agree, concern, stand_aside) in the Homelab Pilot UI - pilot users see conversations and turns only
WHEN naming a user-facing surface THEN use "conversation"; reserve "discussion"/"thread" for Kubemoot internals, the dashboard, and architecture docs

# Operator

DEFINE COMPONENT operator-patterns
DESCRIPTION Go operator with controller-runtime, kubebuilder CRDs

ASSERT CRD version: v1alpha1
ASSERT run tests: make test (Go envtest)
ASSERT after make manifests generate, copy CRDs to chart/kubemoot-operator/crds/ and sync RBAC to chart templates
ASSERT commit zz_generated.deepcopy.go
WHEN adding lifecycle behavior THEN use finalizers and owner refs in the operator
ASSERT see the parent architecture-boundaries component - the operator owns lifecycle/cleanup, the CRD editors do not

DEFINE COMPONENT namespace-lifecycle
DESCRIPTION Namespace management for crew deployments

ASSERT namespace lifecycle is opt-in via kubemoot.ai/manage-namespace annotation
WHEN namespace has kubemoot.ai/crew label THEN operator replicates secrets into it
WHEN Crew CR has manage-namespace annotation THEN operator records the crew on the namespace; it deletes the namespace on CR removal ONLY if the namespace itself carries kubemoot.ai/managed-namespace=true (set by a Namespace-level author, never by the operator)
NEVER delete kube-system, kube-public, kube-node-lease, default, or the operator's own namespace, whatever the labels say
NEVER treat the operator's own kubemoot.ai/crew label as consent to delete
WHEN copying Secrets into crew namespaces THEN sources are the operator namespace plus chart value secretReplication.allowedSourceNamespaces only

# Agent Runtime

DEFINE COMPONENT agent-runtime
DESCRIPTION Quarkus + LangChain4j agent runtime (Java, GraalVM native)

ASSERT run tests: ./gradlew test (JUnit 5, plain - not @QuarkusTest, avoids Ollama dependency)
ASSERT Jackson record deserialization fails silently in GraalVM native - use mapper.readTree() instead
WHEN SmallRye config maps empty string THEN it becomes null (SRCFG00040) - use Optional<String>

# Dashboard

DEFINE COMPONENT persona-dashboard-admin
DESCRIPTION Kubemoot Dashboard audience: admins and developers debugging the agent system

ASSERT dashboard user sees: discussion threads, agent timelines, NATS messages, topology
ASSERT dashboard user understands everything - no information is hidden
