import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ getNode: vi.fn(), readNodeMetrics: vi.fn(), readNodePods: vi.fn(), readPodCounts: vi.fn() }));
vi.mock('#lib/server/k8s/index.js', async () => ({ ...(await import('#lib/server/k8s/errors.js')), getNode: mocks.getNode }));
vi.mock('#lib/server/k8s/node-metrics.js', async (importOriginal) => ({ ...(await importOriginal<object>()), readNodeMetrics: mocks.readNodeMetrics, readNodePods: mocks.readNodePods, readPodCounts: mocks.readPodCounts }));

type Handler = (event: { params: { name: string } }) => Promise<Response>;

beforeEach(() => {
	for (const m of Object.values(mocks)) m.mockReset();
});

describe('GET /api/nodes/[name]', () => {
	it('answers the node with its use and its pods, uncached', async () => {
		mocks.getNode.mockResolvedValue({ metadata: { name: 'n1' } });
		mocks.readNodeMetrics.mockResolvedValue({ available: true, data: { n1: { cpu: '1', memory: '1Gi' } } });
		mocks.readNodePods.mockResolvedValue({ available: false, reason: 'The pod list could not be read' });
		const { GET } = await import('./nodes/[name]/+server');
		const res = await (GET as unknown as Handler)({ params: { name: 'n1' } });
		expect(res.headers.get('Cache-Control')).toBe('no-store');
		expect(await res.json()).toEqual({
			node: { metadata: { name: 'n1' } },
			metrics: { available: true, data: { n1: { cpu: '1', memory: '1Gi' } } },
			pods: { available: false, reason: 'The pod list could not be read' }
		});
		expect(mocks.readNodeMetrics).toHaveBeenCalledWith('n1');
		expect(mocks.readNodePods).toHaveBeenCalledWith('n1');
	});

	it('refuses a name that could change the pod field selector', async () => {
		const { GET } = await import('./nodes/[name]/+server');
		for (const name of ['a,spec.hostNetwork=true', 'a=b', '', 'A b']) {
			const res = await (GET as unknown as Handler)({ params: { name } });
			expect(res.status).toBe(400);
		}
		expect(mocks.readNodePods).not.toHaveBeenCalled();
	});

	it('answers a clear 403 when the account may not read nodes', async () => {
		mocks.getNode.mockRejectedValue({ code: 403 });
		const { GET } = await import('./nodes/[name]/+server');
		const res = await (GET as unknown as Handler)({ params: { name: 'n1' } });
		expect(res.status).toBe(403);
	});

	it('answers an error for a node that is not there', async () => {
		mocks.getNode.mockRejectedValue(new Error('not found'));
		const { GET } = await import('./nodes/[name]/+server');
		const res = await (GET as unknown as Handler)({ params: { name: 'gone' } });
		expect(res.status).toBe(500);
		expect((await res.json()).error).toBe('not found');
	});
});

describe('GET /api/node-usage', () => {
	it('answers use and pod counts, each saying why when it is missing', async () => {
		mocks.readNodeMetrics.mockResolvedValue({ available: false, reason: 'not served' });
		mocks.readPodCounts.mockResolvedValue({ available: true, data: { n1: { running: 1, total: 1, accelerators: 0, holders: [] } } });
		const { GET } = await import('./node-usage/+server');
		const res = await (GET as unknown as () => Promise<Response>)();
		expect(res.headers.get('Cache-Control')).toBe('no-store');
		expect(await res.json()).toEqual({
			metrics: { available: false, reason: 'not served' },
			pods: { available: true, data: { n1: { running: 1, total: 1, accelerators: 0, holders: [] } } }
		});
	});
});
