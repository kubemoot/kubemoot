// Kubemoot CRD TypeScript types based on kubemoot.ai/v1alpha1

export interface Condition {
	type: string;
	status: 'True' | 'False' | 'Unknown';
	reason: string;
	message: string;
	lastTransitionTime: string;
	observedGeneration?: number;
}

// ModelProvider types
export interface ModelProvider {
	apiVersion: string;
	kind: 'ModelProvider';
	metadata: K8sMetadata;
	spec: ModelProviderSpec;
	status?: ModelProviderStatus;
}

export interface ModelProviderSpec {
	type: 'ollama' | 'openai' | 'anthropic';
	endpoint?: string;
	secretRef?: string;
	scheduling?: {
		weight?: number;
	};
}

export interface ModelProviderLoadedModel {
	name: string;
	size?: number;
	sizeVram?: number;
	expiresAt?: string;
}

export interface ModelProviderCapacity {
	availableModels?: { name: string; sizeBytes?: number }[];
	loadedModels?: ModelProviderLoadedModel[];
	maxParallel?: number;
	vramUsedMiB?: number;
	vramTotalMiB?: number;
	nodeName?: string;
	lastProbed?: string;
}

export interface ModelProviderStatus {
	ready?: boolean;
	phase?: string;
	message?: string;
	conditions?: Condition[];
	capacity?: ModelProviderCapacity;
	providerInfo?: {
		version?: string;
		lastChecked?: string;
		rateLimits?: {
			requestsPerMinute?: number;
			tokensPerMinute?: number;
			requestsRemaining?: number;
		};
	};
}

// Model types
export interface Model {
	apiVersion: string;
	kind: 'Model';
	metadata: K8sMetadata;
	spec: ModelSpec;
	status?: ModelStatus;
}

export interface ModelSpec {
	providerRef: string;
	model: string;
	quantization?: string;
	contextLength?: number;
}

export interface ModelStatus {
	ready?: boolean;
	state?: 'Pending' | 'Pulling' | 'Available' | 'Loaded' | 'Error';
	message?: string;
	endpoint?: string;
	conditions?: Condition[];
	modelInfo?: {
		size?: string;
		digest?: string;
		family?: string;
		format?: string;
		parameters?: string;
		quantization?: string;
		contextLength?: number;
		modifiedAt?: string;
	};
}

// EmbeddingModel types
export interface EmbeddingModel {
	apiVersion: string;
	kind: 'EmbeddingModel';
	metadata: K8sMetadata;
	spec: EmbeddingModelSpec;
	status?: EmbeddingModelStatus;
}

export interface EmbeddingModelSpec {
	providerRef: string;
	model: string;
	dimensions?: number;
}

export interface EmbeddingModelStatus {
	ready?: boolean;
	state?: 'Pending' | 'Pulling' | 'Available' | 'Loaded' | 'Error';
	message?: string;
	endpoint?: string;
	conditions?: Condition[];
	modelInfo?: {
		dimensions?: number;
		size?: string;
		digest?: string;
	};
}

// MCPServer types
export interface MCPServer {
	apiVersion: string;
	kind: 'MCPServer';
	metadata: K8sMetadata;
	spec: MCPServerSpec;
	status?: MCPServerStatus;
}

export interface MCPServerSpec {
	image?: string;
	externalEndpoint?: string;
	transport?: 'http' | 'sse' | 'stdio';
	port?: number;
	replicas?: number;
	args?: string[];
	command?: string[];
	env?: EnvVar[];
	secretRef?: string;
	serviceAccountName?: string;
	capabilities?: string[];
	healthPath?: string;
	readinessPath?: string;
	resources?: ResourceRequirements;
	registry?: {
		enabled?: boolean;
		categories?: string[];
		authSecretRef?: string;
	};
}

export interface MCPServerStatus {
	ready?: boolean;
	phase?: string;
	message?: string;
	endpoint?: string;
	replicas?: number;
	availableReplicas?: number;
	conditions?: Condition[];
	tools?: MCPTool[];
	capabilities?: string[];
	discoverable?: boolean;
	registeredWith?: RegistryStatus[];
}

export interface MCPTool {
	name: string;
	description?: string;
}

export interface RegistryStatus {
	gatewayName: string;
	gatewayNamespace: string;
	registered: boolean;
	lastRegistration?: string;
	error?: string;
}

// MCPGateway types
export interface MCPGateway {
	apiVersion: string;
	kind: 'MCPGateway';
	metadata: K8sMetadata;
	spec: MCPGatewaySpec;
	status?: MCPGatewayStatus;
}

export interface MCPGatewaySpec {
	implementation?: 'kubemoot' | 'custom';
	port?: number;
	replicas?: number;
	mcpServerSelector?: {
		matchLabels?: Record<string, string>;
		matchExpressions?: LabelSelectorRequirement[];
	};
	resources?: ResourceRequirements;
}

export interface MCPGatewayStatus {
	ready?: boolean;
	phase?: string;
	message?: string;
	endpoint?: string;
	adminEndpoint?: string;
	conditions?: Condition[];
	registeredServers?: RegisteredServer[];
}

export interface RegisteredServer {
	name: string;
	namespace: string;
	endpoint: string;
	toolCount?: number;
	ready: boolean;
}

// RAGSource types
export interface RAGSource {
	apiVersion: string;
	kind: 'RAGSource';
	metadata: K8sMetadata;
	spec: RAGSourceSpec;
	status?: RAGSourceStatus;
}

export interface RAGSourceSpec {
	embeddingModelRef: string;
	vectorStore: {
		type: 'pgvector' | 'qdrant' | 'milvus';
		endpoint: string;
		secretRef?: string;
		collectionName?: string;
	};
	source: {
		type: 'git' | 'web' | 's3';
		gitUrl?: string;
		branch?: string;
		paths?: string[];
		webUrl?: string;
		s3Bucket?: string;
		s3Prefix?: string;
	};
	chunking?: {
		chunkSize?: number;
		chunkOverlap?: number;
	};
	schedule?: string;
}

export interface RAGSourceStatus {
	ready?: boolean;
	phase?: string;
	message?: string;
	queryEndpoint?: string;
	conditions?: Condition[];
	indexStatus?: {
		documentCount?: number;
		chunkCount?: number;
		lastIndexed?: string;
		indexDuration?: string;
	};
}

// Agent types
export interface Agent {
	apiVersion: string;
	kind: 'Agent';
	metadata: K8sMetadata;
	spec: AgentSpec;
	status?: AgentStatus;
}

export interface AgentSpec {
	type?: 'chat' | 'task' | 'workflow';
	description?: string;
	models: ModelRef[];
	mcpServers?: MCPServerRef[];
	ragSources?: RAGSourceRef[];
	prompt?: {
		system?: string;
		systemConfigMapRef?: { name: string };
		systemConfigMapKey?: string;
		variables?: Record<string, string>;
	};
	inference?: {
		temperature?: string;
		topP?: string;
		topK?: number;
		maxTokens?: number;
		stopSequences?: string[];
	};
	memory?: {
		type?: 'in-memory' | 'redis' | 'postgres';
		endpoint?: string;
		secretRef?: string;
		maxMessages?: number;
		maxTokens?: number;
		ttlSeconds?: number;
	};
	/** @deprecated Use inline guardrails, a2a, and prompt fields instead */
	policyRef?: string;
	guardrails?: {
		readBeforeWrite?: boolean;
		confirmDestructive?: boolean;
		allowedNamespaces?: string[];
		deniedResources?: string[];
		maxToolCallsPerTurn?: number;
		maxTokensPerRequest?: number;
		auditLog?: boolean;
	};
	a2a?: {
		enabled?: boolean;
		role?: 'coordinator' | 'specialist' | 'peer';
		skills?: Array<{ id: string; name?: string; description?: string }>;
		subscribeChannels?: string[];
		maxInferencesPerMinute?: number;
		discussionTimeoutSeconds?: number;
	};
	deployment?: {
		replicas?: number;
		port?: number;
		image?: string;
		serviceAccountName?: string;
		resources?: ResourceRequirements;
		env?: EnvVar[];
	};
}

export interface AgentStatus {
	ready?: boolean;
	phase?: string;
	message?: string;
	endpoint?: string;
	availableReplicas?: number;
	conditions?: Condition[];
	modelStatus?: ModelRefStatus[];
	mcpServerStatus?: MCPServerRefStatus[];
	ragSourceStatus?: RAGSourceRefStatus[];
}

export interface ModelRef {
	name: string;
	role?: 'primary' | 'fallback';
}

export interface MCPServerRef {
	name: string;
	enabledTools?: string[];
	disabledTools?: string[];
}

export interface RAGSourceRef {
	name: string;
	topK?: number;
	priority?: number;
}

export interface ModelRefStatus {
	name: string;
	ready: boolean;
}

export interface MCPServerRefStatus {
	name: string;
	ready: boolean;
	toolCount?: number;
}

export interface RAGSourceRefStatus {
	name: string;
	ready: boolean;
	documentCount?: number;
}

// AgentPolicy types
export interface AgentPolicy {
	apiVersion: string;
	kind: 'AgentPolicy';
	metadata: K8sMetadata;
	spec: AgentPolicySpec;
	status?: AgentPolicyStatus;
}

export interface AgentPolicySpec {
	guardrails?: {
		readBeforeWrite?: boolean;
		confirmDestructive?: boolean;
		allowedNamespaces?: string[];
		deniedResources?: string[];
		maxToolCallsPerTurn?: number;
		maxTokensPerRequest?: number;
		auditLog?: boolean;
	};
	a2a?: {
		enabled?: boolean;
		role?: 'coordinator' | 'specialist' | 'peer';
		skills?: Array<{ id: string; name?: string; description?: string }>;
		subscribeChannels?: string[];
		maxInferencesPerMinute?: number;
		discussionTimeoutSeconds?: number;
	};
	prompt?: {
		system?: string;
	};
	inference?: {
		temperature?: number;
		topP?: number;
		topK?: number;
		maxTokens?: number;
	};
}

export interface AgentPolicyStatus {
	ready?: boolean;
	referencedBy?: string[];
	lastUpdated?: string;
	message?: string;
}

// MCPQualityPolicy types
export interface MCPQualityPolicy {
	apiVersion: string;
	kind: 'MCPQualityPolicy';
	metadata: K8sMetadata;
	spec: MCPQualityPolicySpec;
	status?: MCPQualityPolicyStatus;
}

export interface MCPQualityPolicySpec {
	allowing?: AllowingEntry[];
	blocking?: BlockingEntry[];
	considering?: ConsideringConfig;
}

export interface AllowingEntry {
	name?: string;
	author?: string;
}

export interface BlockingEntry {
	name?: StringMatcher;
	author?: StringMatcher;
	version?: string;
	reason?: string;
}

export interface StringMatcher {
	type?: 'exact' | 'glob' | 'regex';
	value: string;
	negate?: boolean;
}

export interface ConsideringConfig {
	enabled: boolean;
	agentRef: string;
	fallbackAction?: 'allow' | 'deny';
	confidenceThreshold?: string;
	timeoutSeconds?: number;
	criteria?: string;
	criteriaFromConfigMap?: {
		name: string;
		key: string;
	};
}

export interface MCPQualityPolicyStatus {
	conditions?: Condition[];
	serversEvaluated?: number;
	serversAllowed?: number;
	serversBlocked?: number;
	lastEvaluated?: string;
}

// MCPCatalog types
export interface MCPCatalog {
	apiVersion: string;
	kind: 'MCPCatalog';
	metadata: K8sMetadata;
	spec: MCPCatalogSpec;
	status?: MCPCatalogStatus;
}

export interface MCPCatalogSpec {
	type: 'official-registry' | 'smithery' | 'glama' | 'docker' | 'npm' | 'agent';
	url: string;
	syncInterval?: string;
	maxServers?: number;
	queries?: string[];
	qualityPolicyRef?: string;
	agentRef?: string;
	auth?: {
		secretRef?: string;
	};
}

export interface MCPCatalogStatus {
	phase?: string;
	message?: string;
	conditions?: Condition[];
	serversDiscovered?: number;
	serversAllowed?: number;
	serversBlocked?: number;
	lastSync?: string;
	nextSync?: string;
	discoveredServers?: DiscoveredServer[];
}

export interface DiscoveredServer {
	name: string;
	description?: string;
	author?: string;
	version?: string;
	transport?: string;
	categories?: string[];
	githubUrl?: string;
	packageIdentifier?: string;
	registryType?: string;
	qualityDecision?: string;
	qualityReason?: string;
}

// KubemootConfig types
export interface KubemootConfig {
	apiVersion: string;
	kind: 'KubemootConfig';
	metadata: K8sMetadata;
	spec: KubemootConfigSpec;
	status?: KubemootConfigStatus;
}

export interface KubemootConfigSpec {
	defaultImages?: {
		agent?: string;
		mcpGateway?: string;
		ragIndexer?: string;
	};
	defaults?: {
		modelProvider?: string;
		embeddingModel?: string;
		vectorStore?: {
			type?: string;
			endpoint?: string;
		};
	};
}

export interface KubemootConfigStatus {
	ready?: boolean;
	message?: string;
	conditions?: Condition[];
}

// MCPServerReport types (cluster-scoped)
export interface MCPServerReport {
	apiVersion: string;
	kind: 'MCPServerReport';
	metadata: K8sMetadata;
	spec: MCPServerReportSpec;
	status?: MCPServerReportStatus;
}

export interface MCPServerReportSpec {
	serverName?: string;
	githubUrl?: string;
	registryType?: string;
	packageIdentifier?: string;
	adminVerdict?: 'use' | 'caution' | 'avoid' | '';
	adminNotes?: string;
	adminAuthor?: string;
}

export interface MCPServerReportStatus {
	verdict?: 'use' | 'caution' | 'avoid' | 'untested';
	recommendedTransport?: string;
	recommendedVersion?: string;
	successCount?: number;
	failureCount?: number;
	successRate?: string;
	lastTested?: string;
	lastSuccessful?: string;
	trials?: TrialRecord[];
	chroniclerNotes?: string;
	conditions?: Condition[];
}

export interface TrialRecord {
	version: string;
	transport: string;
	image: string;
	testedAt: string;
	phase: 'deploy' | 'connect' | 'initialize' | 'discover' | 'call';
	success: boolean;
	errorMessage: string;
	toolsFound: number;
	setupRecipe?: string;
}

// Common K8s types
export interface K8sMetadata {
	name: string;
	namespace?: string;
	uid?: string;
	resourceVersion?: string;
	creationTimestamp?: string;
	labels?: Record<string, string>;
	annotations?: Record<string, string>;
}

export interface EnvVar {
	name: string;
	value?: string;
	valueFrom?: {
		secretKeyRef?: { name: string; key: string; optional?: boolean };
		configMapKeyRef?: { name: string; key: string; optional?: boolean };
		fieldRef?: { fieldPath: string; apiVersion?: string };
	};
}

export interface ResourceRequirements {
	requests?: Record<string, string>;
	limits?: Record<string, string>;
}

export interface LabelSelectorRequirement {
	key: string;
	operator: 'In' | 'NotIn' | 'Exists' | 'DoesNotExist';
	values?: string[];
}

// Topology types
export interface TopologyNode {
	id: string;
	namespace: string;
	role: 'coordinator' | 'specialist' | 'peer';
	status: 'ready' | 'error' | 'pending';
	endpoint: string;
	toolCount: number;
	peerCount: number;
	description: string;
	labels: Record<string, string>;
}

export interface TopologyEdge {
	id: string;
	source: string;
	target: string;
}

export interface TopologyResponse {
	nodes: TopologyNode[];
	edges: TopologyEdge[];
}

// Discussion types — consensus signals
export interface DiscussionMessage {
	messageId: string;
	threadId: string;
	agentName: string;
	messageType:
		| 'thread_start'
		| 'advisory'
		| 'advisory_ready'
		| 'review_ready'
		| 'agree'
		| 'concern'
		| 'block'
		| 'stand_aside'
		| 'failure'
		| 'proposal'
		| 'consent'
		| 'synthesis'
		| 'follow_up'
		| 'reply'
		| 'thread_close'
		| 'thread_pause'
		| 'thread_resume'
		| 'stop_requested'
		| 'triage_result'
		| 'gap_detected'
		| 'waking'
		| 'ready'
		// Legacy types (backwards compatible)
		| 'contribution'
		| 'decline';
	content: string;
	parentId?: string;
	channel: string;
	timestamp: string;
	metadata?: {
		userQuery?: string;
		confidenceScore?: number;
		toolsUsed?: string[];
		technologies?: string[];
		wisdom?: string;
		advisory?: string;
		primaryChannel?: string;
		synthesisOf?: string[];
		agreeCount?: number;
		concernCount?: number;
		blockCount?: number;
		standAsideCount?: number;
		failureCount?: number;
		signal?: string;
		reason?: string;
		// Failure-signal metadata (when messageType === 'failure'): set by the
		// agent's tool-loop failure path so the dashboard can surface root
		// cause (which tool, which exception, what kind of exhaustion).
		failureType?: 'same_tool_repeated' | 'too_many_tool_failures' | 'iterations_exhausted';
		failedTool?: string;
		failureCount_inLoop?: number;
		lastError?: string;
		// Per-Call Provider Attribution (Card #4 of Epic - JIT GPU Scheduling).
		// The ModelProvider name (e.g. "ollama-gpu", "ollama-rig1") the
		// agent's JIT ProviderSelector chose for THIS specific inference call.
		// Empty/missing when the agent fell back to its static endpoint
		// (NATS unavailable or pre-Card-#2 image). Dashboard renders this
		// in the Agent Summary GPU column to show real per-call placement.
		provider?: string;
		// FitPredictor v2 (2026-05-28): per-call reasoning string from the
		// agent's pickAndClaim decision. Example values:
		//   "warm, slot 1/2 | bootstrap (3 samples)"
		//   "warm, slot 1/2 | SR=0.92 over 23 samples, EMA latency 4200ms"
		//   "cold-load fits (15000 weights + 0 active KV + 2500 this-call KV ≤ 24563 MiB)"
		// Empty for static-fallback calls. Surfaced on the discussion timeline
		// so operators can see WHY a provider was chosen, not just WHICH.
		pickReason?: string;
		onboarding?: boolean;
		gapDetected?: boolean;
		// gap_detected metadata.gapType — distinguishes which kind of gap
		// the coordinator detected so dashboards can render differently
		// (e.g., infrastructure-gap shouldn't suggest "onboard a specialist").
		// Added 2026-05-26 alongside the gap_detected log/metadata-label
		// fix that mis-labelled INFRASTRUCTURE_GAP as SPECIALIST_GAP.
		gapType?: 'TOOL_GAP' | 'INFRASTRUCTURE_GAP' | 'SPECIALIST_GAP';
		modelName?: string;
		gpuLabel?: string;
		inferenceMs?: number;
		loadDurationMs?: number;
		inferenceStartMs?: number;
		inputTokens?: number;
		outputTokens?: number;
		crew?: string;
		// Crew Helm chart version (provenance) from thread_start; the operator sets it
		// from the crew chart's kubemoot.ai/crew-version label. Absent for hand-applied crews.
		crewVersion?: string;
		conversationId?: string;
		// Triage subcommittee fields
		agents?: Array<{ name: string; confidence: number; reason: string }>;
		overallConfidence?: number;
		innerCircle?: string[];
		triageConfidence?: number;
		// Thread close token summary (advisory/triage/synthesis in/out counts)
		totalTokens?: Record<string, { in: number; out: number }>;
		// Replay: source thread that this thread_start is replaying
		replayOf?: string;
	};
}

// Agent heartbeat types (from NATS KV bucket kubemoot_agent_state)
export interface AgentHeartbeat {
	agent: string;
	timestamp: string;
	nats: boolean;
	ollama: boolean;
	model: string;
	lastInference?: string;
}

// Crew types
export interface Crew {
	apiVersion: string;
	kind: 'Crew';
	metadata: K8sMetadata;
	spec: CrewSpec;
	status?: CrewStatus;
}

export interface CrewSpec {
	description?: string;
	discussion?: {
		enabled?: boolean;
		resources?: Record<string, string>;
	};
}

export interface CrewStatus {
	phase?: string;
	ready?: boolean;
	discussionEndpoint?: string;
	coordinatorRef?: string;
	agentCount?: number;
	message?: string;
	conditions?: Condition[];
}

// PromptModule types
export interface PromptModule {
	apiVersion: string;
	kind: 'PromptModule';
	metadata: K8sMetadata;
	spec: PromptModuleSpec;
}

export interface PromptModuleSpec {
	order?: number;
	content?: string;
}

// CrewFitness types
export interface CrewFitness {
	apiVersion: string;
	kind: 'CrewFitness';
	metadata: K8sMetadata;
	spec: CrewFitnessSpec;
	status?: CrewFitnessStatus;
}

export interface CrewFitnessSpec {
	crewRef: string;
	testRef: string;
	configMapRef: string;
	ttl?: string;
}

export interface CrewFitnessAssertionResult {
	raw: string;
	passed: boolean;
	message: string;
}

export interface CrewFitnessStatus {
	phase?: 'Pending' | 'Running' | 'Passed' | 'Failed' | 'Error';
	startedAt?: string;
	completedAt?: string;
	durationMs?: number;
	jobRef?: string;
	assertions?: CrewFitnessAssertionResult[];
	error?: string;
	conditions?: Condition[];
}

// CrewFitnessSuite types — N-iteration sweep of fitness scripts against a
// crew. Operator-side reconciler creates per-iteration CrewFitness CRs
// serially (or up to spec.concurrency in parallel), aggregates results,
// and writes an XLSX artifact to the NATS Object Store. Dashboard reads
// the artifact on download.
export interface CrewFitnessSuite {
	apiVersion: string;
	kind: 'CrewFitnessSuite';
	metadata: K8sMetadata;
	spec: CrewFitnessSuiteSpec;
	status?: CrewFitnessSuiteStatus;
}

export interface SuiteScript {
	testRef: string;
	testContent?: string;
	configMapRef?: string;
}

export interface CrewFitnessSuiteSpec {
	crewRef: string;
	description?: string;
	iterations: number;
	concurrency?: number;
	scripts: SuiteScript[];
	perIterationTimeout?: string;
	artifactRetention?: string;
}

export interface SuiteArtifactRef {
	bucket: string;
	objectKey: string;
	sizeBytes?: number;
}

export interface CrewFitnessSuiteStatus {
	phase?: 'Pending' | 'Running' | 'Completed' | 'Failed' | 'Error';
	runId?: string;
	startedAt?: string;
	completedAt?: string;
	iterationsTotal?: number;
	iterationsCompleted?: number;
	passed?: number;
	failed?: number;
	errored?: number;
	artifactRef?: SuiteArtifactRef;
	error?: string;
	conditions?: Condition[];
}

// List types
export interface KubemootList<T> {
	apiVersion: string;
	kind: string;
	metadata: { resourceVersion?: string };
	items: T[];
}
