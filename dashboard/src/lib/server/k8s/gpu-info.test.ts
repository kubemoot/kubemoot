import { describe, expect, it } from 'vitest';
import type { K8sNode } from '#lib/types/k8s.js';
import { extractGPUInfo } from './gpu-info';

function node(
	capacity: Record<string, string>,
	labels: Record<string, string> = {},
	allocatable: Record<string, string> = {}
): K8sNode {
	return {
		apiVersion: 'v1',
		kind: 'Node',
		metadata: { name: 'n1', labels },
		spec: {},
		status: { capacity, allocatable }
	} as unknown as K8sNode;
}

describe('extractGPUInfo', () => {
	it('reports an NVIDIA GPU with product and memory labels', () => {
		const info = extractGPUInfo(
			node(
				{ 'nvidia.com/gpu': '2', cpu: '16' },
				{ 'nvidia.com/gpu.product': 'RTX-4090', 'nvidia.com/gpu.memory': '24564' },
				{ 'nvidia.com/gpu': '1' }
			)
		);
		expect(info).toEqual({
			present: true,
			type: 'RTX-4090',
			count: 2,
			memory: '24GB',
			capacity: '2',
			allocatable: '1'
		});
	});

	it('falls back to the GPU family label when the product label is absent', () => {
		const info = extractGPUInfo(
			node({ 'nvidia.com/gpu': '1' }, { 'nvidia.com/gpu.family': 'ampere' })
		);
		expect(info.type).toBe('ampere');
		expect(info.memory).toBeUndefined();
	});

	it('finds an AMD GPU and leaves type and memory unset without labels', () => {
		const info = extractGPUInfo(node({ 'amd.com/gpu': '1' }));
		expect(info).toMatchObject({ present: true, count: 1, capacity: '1' });
		expect(info.type).toBeUndefined();
		expect(info.allocatable).toBeUndefined();
	});

	it('skips a GPU resource with zero capacity and uses the next one', () => {
		const info = extractGPUInfo(node({ 'nvidia.com/gpu': '0', 'intel.com/gpu': '3' }));
		expect(info).toMatchObject({ present: true, count: 3 });
	});

	it('reports no GPU when no GPU resource has capacity', () => {
		expect(extractGPUInfo(node({ cpu: '8' }))).toEqual({ present: false });
		expect(extractGPUInfo(node({ 'nvidia.com/gpu': 'x' }))).toEqual({ present: false });
	});

	it('reports no GPU for a node without a status', () => {
		const bare = { metadata: { name: 'n' } } as unknown as K8sNode;
		expect(extractGPUInfo(bare)).toEqual({ present: false });
	});
});
