import type { K8sNode, GPUInfo } from '$types/k8s.js';

const GPU_RESOURCE_KEYS = ['nvidia.com/gpu', 'amd.com/gpu', 'intel.com/gpu'];

/** The GPU model from the NVIDIA feature-discovery labels: product, else family. */
function gpuTypeFromLabels(labels: Record<string, string>): string | undefined {
	return labels['nvidia.com/gpu.product'] || labels['nvidia.com/gpu.family'] || undefined;
}

/** GPU memory from the NVIDIA label (MiB), rounded to whole GB. */
function gpuMemoryFromLabels(labels: Record<string, string>): string | undefined {
	const memory = labels['nvidia.com/gpu.memory'];
	if (!memory) return undefined;
	return `${Math.round(Number.parseInt(memory) / 1024)}GB`;
}

/** The first GPU extended resource the node has a positive capacity of. */
function firstGpuResource(capacity: Record<string, string>): string | undefined {
	return GPU_RESOURCE_KEYS.find((key) => capacity[key] && Number.parseInt(capacity[key]) > 0);
}

/** The node's GPU summary, from its capacity, allocatable, and feature-discovery labels. */
export function extractGPUInfo(node: K8sNode): GPUInfo {
	const capacity = node.status?.capacity || {};
	const key = firstGpuResource(capacity);
	if (!key) return { present: false };

	const labels = node.metadata?.labels || {};
	return {
		present: true,
		type: gpuTypeFromLabels(labels),
		count: Number.parseInt(capacity[key]),
		memory: gpuMemoryFromLabels(labels),
		capacity: capacity[key],
		allocatable: (node.status?.allocatable || {})[key]
	};
}
