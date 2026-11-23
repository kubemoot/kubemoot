import { getCoreApi, getCustomObjectsApi } from './client.js';
import { failedReading, readingOf } from './errors.js';
import type { NodeMetric } from '#lib/node-usage.js';
import type { NodePod, PodCount, Reading } from '#lib/reading.js';
import { ACCELERATOR_RESOURCE_KEYS } from './accelerator-info.js';

export type { NodePod, PodCount, Reading } from '#lib/reading.js';

interface MetricsItem {
	metadata?: { name?: string };
	usage?: NodeMetric;
}

interface PodItem {
	metadata?: { name?: string; namespace?: string };
	spec?: { nodeName?: string; containers?: { resources?: { requests?: Record<string, string>; limits?: Record<string, string> } }[] };
	status?: { phase?: string; containerStatuses?: { restartCount?: number }[] };
}

const METRICS_GROUP = { group: 'metrics.k8s.io', version: 'v1beta1', plural: 'nodes' } as const;

/** A Kubernetes node name: lower-case letters, digits, dashes and dots. Anything else, a comma or an equals sign most of all, would change a field selector. */
export function isNodeName(name: string): boolean {
	return /^[a-z0-9.-]{1,253}$/.test(name);
}

/** CPU and memory use per node from metrics.k8s.io, keyed by node name. */
export async function readNodeMetrics(name?: string): Promise<Reading<Record<string, NodeMetric>>> {
	try {
		const api = getCustomObjectsApi();
		const items: MetricsItem[] = name
			? [await api.getClusterCustomObject({ ...METRICS_GROUP, name })]
			: ((await api.listClusterCustomObject(METRICS_GROUP)) as { items?: MetricsItem[] }).items ?? [];
		const data: Record<string, NodeMetric> = {};
		for (const item of items) if (item.metadata?.name && item.usage) data[item.metadata.name] = item.usage;
		return { available: true, data };
	} catch (e) {
		const none = 'Node metrics (metrics.k8s.io) has no reading for this node, which happens when it is down';
		return failedReading(e, 'node metrics (metrics.k8s.io)', name ? none : undefined);
	}
}

async function listPods(node?: string): Promise<PodItem[]> {
	const api = getCoreApi();
	const result = await api.listPodForAllNamespaces(node ? { fieldSelector: `spec.nodeName=${node}` } : {});
	return result.items;
}

/** A pod that is finished no longer holds a place on its node. */
const FINISHED = new Set(['Succeeded', 'Failed']);

/** Accelerator devices one pod asks for: per container, the larger of its request and limit, summed. */
function acceleratorsRequested(pod: PodItem): number {
	return (pod.spec?.containers ?? []).reduce((sum, c) => {
		const ask = ACCELERATOR_RESOURCE_KEYS.map((key) => Math.max(Number(c.resources?.requests?.[key] ?? 0), Number(c.resources?.limits?.[key] ?? 0)));
		return sum + ask.reduce((a, b) => a + (Number.isFinite(b) ? b : 0), 0);
	}, 0);
}

/** Adds a pod that still holds a place on its node: one more pod, and the devices it holds. */
function holdPlace(entry: PodCount, pod: PodItem): void {
	entry.total++;
	const asked = acceleratorsRequested(pod);
	entry.accelerators += asked;
	if (asked > 0) entry.holders.push({ namespace: pod.metadata?.namespace ?? '', name: pod.metadata?.name ?? '' });
}

/** Adds one pod to its node's count. */
function addPod(entry: PodCount, pod: PodItem): void {
	const phase = pod.status?.phase ?? 'Unknown';
	if (!FINISHED.has(phase)) holdPlace(entry, pod);
	if (phase === 'Running') entry.running++;
}

/** How many pods run on each node, keyed by node name. */
export function countPods(pods: readonly PodItem[]): Record<string, PodCount> {
	const counts: Record<string, PodCount> = {};
	for (const pod of pods) {
		const node = pod.spec?.nodeName;
		if (!node) continue;
		counts[node] ??= { running: 0, total: 0, accelerators: 0, holders: [] };
		addPod(counts[node], pod);
	}
	return counts;
}

function restartsOf(pod: PodItem): number {
	return (pod.status?.containerStatuses ?? []).reduce((n, c) => n + (c.restartCount ?? 0), 0);
}

function toNodePod(pod: PodItem): NodePod {
	const phase = pod.status?.phase ?? 'Unknown';
	return {
		name: pod.metadata?.name ?? '',
		namespace: pod.metadata?.namespace ?? '',
		phase,
		accelerators: FINISHED.has(phase) ? 0 : acceleratorsRequested(pod),
		restarts: restartsOf(pod)
	};
}

/** The pods of one node: name, namespace, phase and the restarts of all containers. */
export function podsOnNode(pods: readonly PodItem[]): NodePod[] {
	return pods.map(toNodePod).sort((a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name));
}

/** Pod counts for every node. */
export function readPodCounts(): Promise<Reading<Record<string, PodCount>>> {
	return readingOf('the pod list', async () => countPods(await listPods()));
}

/** The pods on one node. */
export function readNodePods(node: string): Promise<Reading<NodePod[]>> {
	return readingOf('the pod list', async () => podsOnNode(await listPods(node)));
}
