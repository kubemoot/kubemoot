import { getCoreApi } from './client.js';
import type { K8sNode, NodeWithGPU, GPUInfo } from '$types/k8s.js';

const GPU_RESOURCE_KEYS = [
	'nvidia.com/gpu',
	'amd.com/gpu',
	'intel.com/gpu'
];

function extractGPUInfo(node: K8sNode): GPUInfo {
	const capacity = node.status?.capacity || {};
	const allocatable = node.status?.allocatable || {};
	const labels = node.metadata?.labels || {};

	// Check for GPU resources
	for (const key of GPU_RESOURCE_KEYS) {
		const capacityCount = capacity[key];
		const allocatableCount = allocatable[key];

		if (capacityCount && parseInt(capacityCount) > 0) {
			// Try to determine GPU type from labels
			let gpuType: string | undefined;

			// NVIDIA GPU labels
			if (labels['nvidia.com/gpu.product']) {
				gpuType = labels['nvidia.com/gpu.product'];
			} else if (labels['nvidia.com/gpu.family']) {
				gpuType = labels['nvidia.com/gpu.family'];
			}

			// GPU memory from labels (NVIDIA)
			let gpuMemory: string | undefined;
			if (labels['nvidia.com/gpu.memory']) {
				const memMB = parseInt(labels['nvidia.com/gpu.memory']);
				gpuMemory = `${Math.round(memMB / 1024)}GB`;
			}

			return {
				present: true,
				type: gpuType,
				count: parseInt(capacityCount),
				memory: gpuMemory,
				capacity: capacityCount,
				allocatable: allocatableCount
			};
		}
	}

	return { present: false };
}

export async function listNodes(): Promise<NodeWithGPU[]> {
	const api = getCoreApi();
	const nodeList = await api.listNode();

	return (nodeList.items as unknown as K8sNode[]).map((node) => ({
		...node,
		gpu: extractGPUInfo(node)
	}));
}

export async function getNode(name: string): Promise<NodeWithGPU> {
	const api = getCoreApi();
	const node = await api.readNode({ name }) as unknown as K8sNode;

	return {
		...node,
		gpu: extractGPUInfo(node)
	};
}

export async function listNamespaces(): Promise<string[]> {
	const api = getCoreApi();
	const nsList = await api.listNamespace();

	return nsList.items
		.map((ns: { metadata?: { name?: string } }) => ns.metadata?.name)
		.filter((name: string | undefined): name is string => !!name)
		.sort();
}

/** A crew and the namespace it's deployed to. */
export interface CrewNamespace {
	namespace: string;
	crew: string;
}

// listCrewNamespaces returns only the namespaces that host a Kubemoot crew —
// those carrying the `kubemoot.ai/crew` label — paired with the crew name. The
// dashboard's top-bar selector scopes pages by crew, so non-crew namespaces
// (kube-system, flux-system, the shared kubemoot control-plane ns, …) are noise
// there and excluded. "All crews" (no selection) still shows everything.
export async function listCrewNamespaces(): Promise<CrewNamespace[]> {
	const api = getCoreApi();
	const nsList = await api.listNamespace();

	return nsList.items
		.map((ns: { metadata?: { name?: string; labels?: Record<string, string> } }) => ({
			namespace: ns.metadata?.name ?? '',
			crew: ns.metadata?.labels?.['kubemoot.ai/crew'] ?? ''
		}))
		.filter((e: CrewNamespace) => e.namespace !== '' && e.crew !== '')
		.sort((a: CrewNamespace, b: CrewNamespace) => a.crew.localeCompare(b.crew));
}
