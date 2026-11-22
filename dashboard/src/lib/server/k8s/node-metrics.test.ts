import { beforeEach, describe, expect, it, vi } from 'vitest';

const getClusterCustomObject = vi.fn();
const listClusterCustomObject = vi.fn();
const listPodForAllNamespaces = vi.fn();

vi.mock('./client.js', () => ({
	getCustomObjectsApi: () => ({ getClusterCustomObject, listClusterCustomObject }),
	getCoreApi: () => ({ listPodForAllNamespaces })
}));

const { countPods, podsOnNode, readNodeMetrics, readNodePods, readPodCounts } = await import('./node-metrics');

const pod = (name: string, node: string, phase: string, over: { restarts?: number[]; gpu?: string } = {}) => ({
	metadata: { name, namespace: 'ns' },
	spec: { nodeName: node, containers: [{ resources: over.gpu ? { limits: { 'nvidia.com/gpu': over.gpu } } : {} }] },
	status: { phase, containerStatuses: (over.restarts ?? []).map((restartCount) => ({ restartCount })) }
});

beforeEach(() => {
	for (const m of [getClusterCustomObject, listClusterCustomObject, listPodForAllNamespaces]) m.mockReset();
});

describe('countPods', () => {
	it('counts running pods and the pods that still hold a place, per node, with the accelerators they ask for', () => {
		const counts = countPods([pod('a', 'n1', 'Running', { gpu: '1' }), pod('b', 'n1', 'Pending'), pod('c', 'n1', 'Succeeded', { gpu: '1' }), pod('d', 'n2', 'Running'), { metadata: { name: 'e' } }]);
		expect(counts).toEqual({ n1: { running: 1, total: 2, accelerators: 1 }, n2: { running: 1, total: 1, accelerators: 0 } });
	});
});

describe('podsOnNode', () => {
	it('lists name, namespace, phase and the restarts of all containers, sorted', () => {
		const out = podsOnNode([pod('b', 'n1', 'Running', { restarts: [2, 1] }), pod('a', 'n1', 'Failed')]);
		expect(out).toEqual([
			{ name: 'a', namespace: 'ns', phase: 'Failed', restarts: 0, accelerators: 0 },
			{ name: 'b', namespace: 'ns', phase: 'Running', restarts: 3, accelerators: 0 }
		]);
	});
});

describe('readNodeMetrics', () => {
	it('reads use per node from metrics.k8s.io', async () => {
		listClusterCustomObject.mockResolvedValue({ items: [{ metadata: { name: 'n1' }, usage: { cpu: '250m', memory: '1Gi' } }, { metadata: { name: 'n2' } }] });
		expect(await readNodeMetrics()).toEqual({ available: true, data: { n1: { cpu: '250m', memory: '1Gi' } } });
		expect(listClusterCustomObject).toHaveBeenCalledWith({ group: 'metrics.k8s.io', version: 'v1beta1', plural: 'nodes' });
	});

	it('reads one node by name', async () => {
		getClusterCustomObject.mockResolvedValue({ metadata: { name: 'n1' }, usage: { cpu: '1', memory: '2Gi' } });
		expect(await readNodeMetrics('n1')).toEqual({ available: true, data: { n1: { cpu: '1', memory: '2Gi' } } });
	});

	it('says the cluster does not serve metrics, not that use is zero', async () => {
		listClusterCustomObject.mockRejectedValue({ code: 404 });
		expect(await readNodeMetrics()).toEqual({ available: false, reason: 'Node metrics (metrics.k8s.io) is not served by this cluster' });
	});

	it('says a node has no reading, not that the cluster lacks metrics, when one node is not found', async () => {
		getClusterCustomObject.mockRejectedValue({ code: 404 });
		const out = await readNodeMetrics('n1');
		expect((out as { reason: string }).reason).toMatch(/no reading for this node/);
	});

	it('says the account may not read them', async () => {
		listClusterCustomObject.mockRejectedValue({ code: 403 });
		const out = await readNodeMetrics();
		expect(out.available).toBe(false);
		expect((out as { reason: string }).reason).toMatch(/permission/);
	});

	it('gives a plain reason for any other failure', async () => {
		listClusterCustomObject.mockRejectedValue(new Error('boom'));
		expect(await readNodeMetrics()).toEqual({ available: false, reason: 'Node metrics (metrics.k8s.io) could not be read' });
	});
});

describe('pod reads', () => {
	it('lists the pods of one node with a field selector', async () => {
		listPodForAllNamespaces.mockResolvedValue({ items: [pod('a', 'n1', 'Running')] });
		const out = await readNodePods('n1');
		expect(listPodForAllNamespaces).toHaveBeenCalledWith({ fieldSelector: 'spec.nodeName=n1' });
		expect(out).toMatchObject({ available: true, data: [{ name: 'a' }] });
	});

	it('counts pods for every node, and says why when it may not', async () => {
		listPodForAllNamespaces.mockResolvedValueOnce({ items: [pod('a', 'n1', 'Running')] });
		expect(await readPodCounts()).toEqual({ available: true, data: { n1: { running: 1, total: 1, accelerators: 0 } } });
		listPodForAllNamespaces.mockRejectedValueOnce({ code: 403 });
		expect((await readPodCounts()).available).toBe(false);
	});
});
