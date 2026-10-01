import { getCustomObjectsApi } from './client.js';
import { setHeaderOptions } from '@kubernetes/client-node/dist/middleware.js';
import type {
	ModelProvider,
	Model,
	EmbeddingModel,
	MCPServer,
	MCPGateway,
	MCPQualityPolicy,
	MCPCatalog,
	MCPServerReport,
	RAGSource,
	Agent,
	KubemootConfig,
	Crew,
	CrewFitness,
	CrewFitnessSuite,
	PromptModule,
	KubemootList
} from '$types/kubemoot.js';

const GROUP = 'kubemoot.ai';
const VERSION = 'v1alpha1';

// CRD resource definitions
export const KUBEMOOT_CRDS = {
	modelproviders: { plural: 'modelproviders', kind: 'ModelProvider' },
	models: { plural: 'models', kind: 'Model' },
	embeddingmodels: { plural: 'embeddingmodels', kind: 'EmbeddingModel' },
	mcpservers: { plural: 'mcpservers', kind: 'MCPServer' },
	mcpgateways: { plural: 'mcpgateways', kind: 'MCPGateway' },
	mcpqualitypolicies: { plural: 'mcpqualitypolicies', kind: 'MCPQualityPolicy' },
	mcpcatalogs: { plural: 'mcpcatalogs', kind: 'MCPCatalog' },
	ragsources: { plural: 'ragsources', kind: 'RAGSource' },
	agents: { plural: 'agents', kind: 'Agent' },
	mcpserverreports: { plural: 'mcpserverreports', kind: 'MCPServerReport' },
	kubemootconfigs: { plural: 'kubemootconfigs', kind: 'KubemootConfig' },
	crews: { plural: 'crews', kind: 'Crew' },
	crewfitnesses: { plural: 'crewfitnesses', kind: 'CrewFitness' },
	crewfitnesssuites: { plural: 'crewfitnesssuites', kind: 'CrewFitnessSuite' },
	promptmodules: { plural: 'promptmodules', kind: 'PromptModule' }
} as const;

type CRDName = keyof typeof KUBEMOOT_CRDS;

// 429 retry: Kubernetes API server returns 429 with a retry-after hint when
// storage is (re)initializing or the priority/fairness queue is full. Honor it.
// We retry up to MAX_K8S_RETRIES times, doubling our base delay each attempt.
const MAX_K8S_RETRIES = 3;
const BASE_RETRY_DELAY_MS = 500;

interface MaybeK8sError {
	code?: number;
	statusCode?: number;
	response?: { statusCode?: number; headers?: Record<string, string | string[] | undefined> };
	message?: string;
}

function isThrottled(err: unknown): boolean {
	const e = err as MaybeK8sError;
	const code = e?.code ?? e?.statusCode ?? e?.response?.statusCode;
	if (code === 429) return true;
	const msg = e?.message ?? '';
	return /\b429\b|TooManyRequests|storage is .re.initializing/i.test(msg);
}

function retryAfterMs(err: unknown, attempt: number): number {
	const headers = (err as MaybeK8sError)?.response?.headers;
	const raw = headers?.['retry-after'] ?? headers?.['Retry-After'];
	const headerVal = Array.isArray(raw) ? raw[0] : raw;
	const headerSeconds = headerVal ? Number(headerVal) : NaN;
	if (Number.isFinite(headerSeconds) && headerSeconds > 0) {
		return Math.min(5000, headerSeconds * 1000);
	}
	return BASE_RETRY_DELAY_MS * Math.pow(2, attempt);
}

async function withK8sRetry<T>(op: () => Promise<T>): Promise<T> {
	let lastErr: unknown;
	for (let attempt = 0; attempt < MAX_K8S_RETRIES; attempt++) {
		try {
			return await op();
		} catch (e) {
			if (!isThrottled(e) || attempt === MAX_K8S_RETRIES - 1) {
				throw e;
			}
			lastErr = e;
			await new Promise((r) => setTimeout(r, retryAfterMs(e, attempt)));
		}
	}
	throw lastErr;
}

// Generic list function
async function listNamespacedCRD<T>(
	plural: string,
	namespace: string
): Promise<KubemootList<T>> {
	const api = getCustomObjectsApi();
	const result = await withK8sRetry(() =>
		api.listNamespacedCustomObject({
			group: GROUP,
			version: VERSION,
			namespace,
			plural
		})
	);
	return result as KubemootList<T>;
}

// Generic get function
async function getNamespacedCRD<T>(
	plural: string,
	namespace: string,
	name: string
): Promise<T> {
	const api = getCustomObjectsApi();
	const result = await withK8sRetry(() =>
		api.getNamespacedCustomObject({
			group: GROUP,
			version: VERSION,
			namespace,
			plural,
			name
		})
	);
	return result as T;
}

// Generic cluster-scoped list function
async function listClusterCRD<T>(plural: string): Promise<KubemootList<T>> {
	const api = getCustomObjectsApi();
	const result = await withK8sRetry(() =>
		api.listClusterCustomObject({
			group: GROUP,
			version: VERSION,
			plural
		})
	);
	return result as KubemootList<T>;
}

// Generic cluster-scoped get function
async function getClusterCRD<T>(plural: string, name: string): Promise<T> {
	const api = getCustomObjectsApi();
	const result = await withK8sRetry(() =>
		api.getClusterCustomObject({
			group: GROUP,
			version: VERSION,
			plural,
			name
		})
	);
	return result as T;
}

// ModelProvider functions
export async function listModelProviders(namespace: string): Promise<KubemootList<ModelProvider>> {
	if (namespace === '') {
		return listClusterCRD<ModelProvider>('modelproviders');
	}
	return listNamespacedCRD<ModelProvider>('modelproviders', namespace);
}

export async function getModelProvider(namespace: string, name: string): Promise<ModelProvider> {
	return getNamespacedCRD<ModelProvider>('modelproviders', namespace, name);
}

// Model functions
export async function listModels(namespace: string): Promise<KubemootList<Model>> {
	if (namespace === '') {
		return listClusterCRD<Model>('models');
	}
	return listNamespacedCRD<Model>('models', namespace);
}

export async function getModel(namespace: string, name: string): Promise<Model> {
	return getNamespacedCRD<Model>('models', namespace, name);
}

// EmbeddingModel functions
export async function listEmbeddingModels(namespace: string): Promise<KubemootList<EmbeddingModel>> {
	if (namespace === '') {
		return listClusterCRD<EmbeddingModel>('embeddingmodels');
	}
	return listNamespacedCRD<EmbeddingModel>('embeddingmodels', namespace);
}

export async function getEmbeddingModel(namespace: string, name: string): Promise<EmbeddingModel> {
	return getNamespacedCRD<EmbeddingModel>('embeddingmodels', namespace, name);
}

// MCPServer functions
export async function listMCPServers(namespace: string): Promise<KubemootList<MCPServer>> {
	if (namespace === '') {
		return listClusterCRD<MCPServer>('mcpservers');
	}
	return listNamespacedCRD<MCPServer>('mcpservers', namespace);
}

export async function getMCPServer(namespace: string, name: string): Promise<MCPServer> {
	return getNamespacedCRD<MCPServer>('mcpservers', namespace, name);
}

// MCPGateway functions
export async function listMCPGateways(namespace: string): Promise<KubemootList<MCPGateway>> {
	if (namespace === '') {
		return listClusterCRD<MCPGateway>('mcpgateways');
	}
	return listNamespacedCRD<MCPGateway>('mcpgateways', namespace);
}

export async function getMCPGateway(namespace: string, name: string): Promise<MCPGateway> {
	return getNamespacedCRD<MCPGateway>('mcpgateways', namespace, name);
}

// MCPQualityPolicy functions
export async function listMCPQualityPolicies(namespace: string): Promise<KubemootList<MCPQualityPolicy>> {
	if (namespace === '') {
		return listClusterCRD<MCPQualityPolicy>('mcpqualitypolicies');
	}
	return listNamespacedCRD<MCPQualityPolicy>('mcpqualitypolicies', namespace);
}

export async function getMCPQualityPolicy(namespace: string, name: string): Promise<MCPQualityPolicy> {
	return getNamespacedCRD<MCPQualityPolicy>('mcpqualitypolicies', namespace, name);
}

// MCPCatalog functions
export async function listMCPCatalogs(namespace: string): Promise<KubemootList<MCPCatalog>> {
	if (namespace === '') {
		return listClusterCRD<MCPCatalog>('mcpcatalogs');
	}
	return listNamespacedCRD<MCPCatalog>('mcpcatalogs', namespace);
}

export async function getMCPCatalog(namespace: string, name: string): Promise<MCPCatalog> {
	return getNamespacedCRD<MCPCatalog>('mcpcatalogs', namespace, name);
}

// RAGSource functions
export async function listRAGSources(namespace: string): Promise<KubemootList<RAGSource>> {
	if (namespace === '') {
		return listClusterCRD<RAGSource>('ragsources');
	}
	return listNamespacedCRD<RAGSource>('ragsources', namespace);
}

export async function getRAGSource(namespace: string, name: string): Promise<RAGSource> {
	return getNamespacedCRD<RAGSource>('ragsources', namespace, name);
}

// Agent functions
export async function listAgents(namespace: string): Promise<KubemootList<Agent>> {
	if (namespace === '') {
		return listClusterCRD<Agent>('agents');
	}
	return listNamespacedCRD<Agent>('agents', namespace);
}

export async function getAgent(namespace: string, name: string): Promise<Agent> {
	return getNamespacedCRD<Agent>('agents', namespace, name);
}

// MCPServerReport functions (namespaced)
export async function listMCPServerReports(): Promise<KubemootList<MCPServerReport>> {
	return listClusterCRD<MCPServerReport>('mcpserverreports');
}

export async function getMCPServerReport(namespace: string, name: string): Promise<MCPServerReport> {
	return getNamespacedCRD<MCPServerReport>('mcpserverreports', namespace, name);
}

export async function patchMCPServerReport(namespace: string, name: string, patch: Record<string, unknown>): Promise<unknown> {
	const api = getCustomObjectsApi();
	const result = await api.patchNamespacedCustomObject({
		group: GROUP,
		version: VERSION,
		namespace,
		plural: 'mcpserverreports',
		name,
		body: patch
	}, setHeaderOptions('Content-Type', 'application/merge-patch+json'));
	return result;
}

// Crew functions
export async function listCrews(namespace: string): Promise<KubemootList<Crew>> {
	if (namespace === '') {
		return listClusterCRD<Crew>('crews');
	}
	return listNamespacedCRD<Crew>('crews', namespace);
}

export async function getCrew(namespace: string, name: string): Promise<Crew> {
	return getNamespacedCRD<Crew>('crews', namespace, name);
}

// CrewFitness functions
export async function listCrewFitnesses(namespace: string): Promise<KubemootList<CrewFitness>> {
	if (namespace === '') {
		return listClusterCRD<CrewFitness>('crewfitnesses');
	}
	return listNamespacedCRD<CrewFitness>('crewfitnesses', namespace);
}

export async function getCrewFitness(namespace: string, name: string): Promise<CrewFitness> {
	return getNamespacedCRD<CrewFitness>('crewfitnesses', namespace, name);
}

// CrewFitnessSuite functions — namespaced. List supports all-namespace
// (empty namespace param) for the dashboard's cluster-wide view.
export async function listCrewFitnessSuites(namespace: string): Promise<KubemootList<CrewFitnessSuite>> {
	if (namespace === '') {
		return listClusterCRD<CrewFitnessSuite>('crewfitnesssuites');
	}
	return listNamespacedCRD<CrewFitnessSuite>('crewfitnesssuites', namespace);
}

export async function getCrewFitnessSuite(namespace: string, name: string): Promise<CrewFitnessSuite> {
	return getNamespacedCRD<CrewFitnessSuite>('crewfitnesssuites', namespace, name);
}

// deleteCrewFitnessSuite removes a suite run. Owned children (CrewFitness CRs) are
// garbage-collected via owner refs; the operator's finalizer purges the run's NATS
// artifacts (transcripts + score sidecars).
export async function deleteCrewFitnessSuite(namespace: string, name: string): Promise<unknown> {
	const api = getCustomObjectsApi();
	return api.deleteNamespacedCustomObject({
		group: GROUP,
		version: VERSION,
		namespace,
		plural: 'crewfitnesssuites',
		name
	});
}

// patchCrewFitnessSuiteSpec merge-patches the suite's control fields
// (spec.suspend pauses between iterations, spec.cancel stops the suite). The
// operator acts on them; the dashboard only flips the fields.
export async function patchCrewFitnessSuiteSpec(
	namespace: string,
	name: string,
	patch: { spec: { suspend?: boolean; cancel?: boolean } }
): Promise<unknown> {
	const api = getCustomObjectsApi();
	return api.patchNamespacedCustomObject({
		group: GROUP,
		version: VERSION,
		namespace,
		plural: 'crewfitnesssuites',
		name,
		body: patch
	}, setHeaderOptions('Content-Type', 'application/merge-patch+json'));
}

// KubemootConfig functions (cluster-scoped)
export async function listKubemootConfigs(): Promise<KubemootList<KubemootConfig>> {
	return listClusterCRD<KubemootConfig>('kubemootconfigs');
}

export async function getKubemootConfig(name: string): Promise<KubemootConfig> {
	return getClusterCRD<KubemootConfig>('kubemootconfigs', name);
}

// PromptModule functions (namespaced)
export async function listPromptModules(namespace: string): Promise<KubemootList<PromptModule>> {
	if (namespace === '') {
		return listClusterCRD<PromptModule>('promptmodules');
	}
	return listNamespacedCRD<PromptModule>('promptmodules', namespace);
}

export async function getPromptModule(namespace: string, name: string): Promise<PromptModule> {
	return getNamespacedCRD<PromptModule>('promptmodules', namespace, name);
}

// List all CRDs across all namespaces: the cluster-scoped list call returns the
// objects of every namespace.
export async function listAllNamespacedCRD<T>(plural: string): Promise<KubemootList<T>> {
	return listClusterCRD<T>(plural);
}

// Generic function to get any Kubemoot CRD by type
export async function getKubemootCRD(
	crdType: CRDName,
	namespace: string | null,
	name?: string
): Promise<unknown> {
	const crd = KUBEMOOT_CRDS[crdType];

	if (crdType === 'kubemootconfigs') {
		// Cluster-scoped
		if (name) {
			return getClusterCRD(crd.plural, name);
		}
		return listClusterCRD(crd.plural);
	}

	if (crdType === 'mcpserverreports') {
		// Namespaced but list across all namespaces
		if (name && namespace) {
			return getNamespacedCRD(crd.plural, namespace, name);
		}
		return listClusterCRD(crd.plural);
	}

	// Namespaced
	if (!namespace) {
		return listAllNamespacedCRD(crd.plural);
	}

	if (name) {
		return getNamespacedCRD(crd.plural, namespace, name);
	}

	return listNamespacedCRD(crd.plural, namespace);
}
