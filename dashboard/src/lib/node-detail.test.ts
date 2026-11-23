import { describe, expect, it } from 'vitest';
import { capacityRows, conditionRows, podPhaseOk, staticFacts, taintText } from './node-detail';

const node = {
	metadata: { name: 'n1', creationTimestamp: '2026-11-01T00:00:00Z' },
	status: {
		conditions: [
			{ type: 'Ready', status: 'True' as const, reason: 'KubeletReady' },
			{ type: 'MemoryPressure', status: 'True' as const, reason: 'KubeletHasInsufficientMemory', lastTransitionTime: '2026-11-22T10:00:00Z' },
			{ type: 'DiskPressure', status: 'False' as const },
			{ type: 'NetworkUnavailable', status: 'False' as const }
		],
		capacity: { cpu: '8', memory: '16Gi', pods: '110', 'ephemeral-storage': '100Gi', 'nvidia.com/gpu': '1' },
		allocatable: { cpu: '7900m', memory: '15Gi', pods: '110', 'ephemeral-storage': '90Gi' },
		addresses: [{ type: 'InternalIP' as const, address: 'node-address-1' }, { type: 'Hostname' as const, address: 'n1' }],
		nodeInfo: { osImage: 'Talos', kernelVersion: '6.18', containerRuntimeVersion: 'containerd://2.1', kubeletVersion: 'v1.37.1', architecture: 'amd64' }
	}
};

describe('conditionRows', () => {
	it('lists the conditions that are a problem first, with their reasons and times', () => {
		const rows = conditionRows(node);
		expect(rows.map((r) => [r.type, r.problem])).toEqual([['MemoryPressure', true], ['DiskPressure', false], ['NetworkUnavailable', false], ['Ready', false]]);
		expect(rows[0]).toMatchObject({ reason: 'KubeletHasInsufficientMemory', lastTransitionTime: '2026-11-22T10:00:00Z' });
	});

	it('treats a Ready that is not True as the problem', () => {
		const rows = conditionRows({ status: { conditions: [{ type: 'Ready', status: 'Unknown' }] } } as never);
		expect(rows[0].problem).toBe(true);
	});

	it('is empty for a node without conditions', () => {
		expect(conditionRows({} as never)).toEqual([]);
	});
});

describe('capacityRows', () => {
	it('sets capacity against allocatable against use where use is measured', () => {
		const rows = capacityRows(node, { cpu: '2', memory: '7.5Gi' }, 30);
		expect(rows.map((r) => r.resource)).toEqual(['CPU', 'Memory', 'Pods', 'Ephemeral storage']);
		expect(rows[0]).toEqual({ resource: 'CPU', capacity: '8 cores', allocatable: '7.9 cores', inUse: '2 cores (25%)' });
		expect(rows[1]).toMatchObject({ capacity: '16 GiB', allocatable: '15 GiB', inUse: '7.5 GiB (50%)' });
		expect(rows[2]).toMatchObject({ capacity: '110', inUse: '30 (27%)' });
		expect(rows[3].inUse).toBe('');
	});

	it('leaves use empty, not zero, without readings', () => {
		const rows = capacityRows(node, undefined, undefined);
		expect(rows.map((r) => r.inUse)).toEqual(['', '', '', '']);
	});
});

describe('capacityRows on a node that is not Ready', () => {
	it('says the pods are unknown instead of showing a count', () => {
		const off = { ...node, status: { ...node.status, conditions: [{ type: 'Ready', status: 'Unknown' }] } };
		const pods = capacityRows(off as never, undefined, 6).find((r) => r.resource === 'Pods');
		expect(pods?.inUse).toBe('unknown while the node is offline');
	});

	it('counts the pods on a Ready node', () => {
		const ready = { ...node, status: { ...node.status, conditions: [{ type: 'Ready', status: 'True' }] } };
		expect(capacityRows(ready as never, undefined, 11).find((r) => r.resource === 'Pods')?.inUse).toBe('11 (10%)');
	});
});

describe('static facts and taints', () => {
	it('lists the facts that do not change, leaving out what the node does not say', () => {
		const facts = staticFacts(node as never);
		expect(facts).toContainEqual({ label: 'Internal IP', value: 'node-address-1' });
		expect(facts).toContainEqual({ label: 'Kernel', value: '6.18' });
		expect(facts.map((f) => f.label)).not.toContain('External IP');
	});

	it('writes a taint as key=value:Effect', () => {
		expect(taintText({ key: 'node.kubernetes.io/unreachable', effect: 'NoExecute' })).toBe('node.kubernetes.io/unreachable:NoExecute');
		expect(taintText({ key: 'gpu', value: 'true', effect: 'NoSchedule' })).toBe('gpu=true:NoSchedule');
	});

	it('calls Running and Succeeded pods fine', () => {
		expect(['Running', 'Succeeded', 'Pending', 'Failed', 'Unknown'].filter(podPhaseOk)).toEqual(['Running', 'Succeeded']);
	});
});
