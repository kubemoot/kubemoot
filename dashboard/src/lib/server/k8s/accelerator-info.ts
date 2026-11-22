import type { K8sNode, AcceleratorInfo } from '#lib/types/k8s.js';

// Extended resources that device plugins use to report accelerators.
export const ACCELERATOR_RESOURCE_KEYS = [
	'nvidia.com/gpu',
	'amd.com/gpu',
	'intel.com/gpu',
	'gpu.intel.com/i915',
	'gpu.intel.com/xe',
	'google.com/tpu',
	'habana.ai/gaudi'
];

/** The accelerator model from the feature-discovery labels: NVIDIA product, AMD product name, else NVIDIA family. */
function typeFromLabels(labels: Record<string, string>): string | undefined {
	return (
		labels['nvidia.com/gpu.product'] ||
		labels['amd.com/gpu.product-name'] ||
		labels['nvidia.com/gpu.family'] ||
		undefined
	);
}

/** Accelerator memory from the NVIDIA label (MiB), rounded to whole GB. */
function memoryFromLabels(labels: Record<string, string>): string | undefined {
	const memory = labels['nvidia.com/gpu.memory'];
	if (!memory) return undefined;
	return `${Math.round(Number.parseInt(memory) / 1024)}GB`;
}

/** The first accelerator extended resource the node has a positive capacity of. */
function firstAcceleratorResource(capacity: Record<string, string>): string | undefined {
	return ACCELERATOR_RESOURCE_KEYS.find((key) => capacity[key] && Number.parseInt(capacity[key]) > 0);
}

/** The node's accelerator summary, from its capacity, allocatable, and feature-discovery labels. */
export function extractAcceleratorInfo(node: K8sNode): AcceleratorInfo {
	const capacity = node.status?.capacity || {};
	const key = firstAcceleratorResource(capacity);
	if (!key) return { present: false };

	const labels = node.metadata?.labels || {};
	return {
		present: true,
		type: typeFromLabels(labels),
		count: Number.parseInt(capacity[key]),
		memory: memoryFromLabels(labels),
		capacity: capacity[key],
		allocatable: node.status?.allocatable?.[key]
	};
}
