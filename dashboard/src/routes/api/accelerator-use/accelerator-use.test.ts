import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ listNodes: vi.fn(), readAcceleratorUse: vi.fn() }));
vi.mock('#lib/server/k8s/index.js', () => ({ listNodes: mocks.listNodes }));
vi.mock('#lib/server/accelerator-metrics/index.js', async (orig) => ({ ...(await orig<object>()), readAcceleratorUse: mocks.readAcceleratorUse }));

import { GET } from './+server';

const call = () => (GET as unknown as () => Promise<Response>)();

beforeEach(() => {
	mocks.listNodes.mockReset();
	mocks.readAcceleratorUse.mockReset();
});
afterEach(() => vi.unstubAllEnvs());

describe('GET /api/accelerator-use', () => {
	it('says it is not configured, and reads nothing, when no metrics source is set', async () => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_PROMETHEUS_URL', '');
		expect(await (await call()).json()).toEqual({ configured: false });
		expect(mocks.listNodes).not.toHaveBeenCalled();
		expect(mocks.readAcceleratorUse).not.toHaveBeenCalled();
	});

	it('answers only for the nodes the caller can list', async () => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_PROMETHEUS_URL', 'https://prom.example.test:9090');
		mocks.listNodes.mockResolvedValue([{ metadata: { name: 'a' } }]);
		mocks.readAcceleratorUse.mockResolvedValue({ a: { devices: 1, busy: 0.5 }, b: { devices: 1, busy: 1 } });
		expect(await (await call()).json()).toEqual({ configured: true, use: { available: true, data: { a: { devices: 1, busy: 0.5 } } } });
	});

	it('says why when the metrics source cannot be read, never a zero', async () => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_PROMETHEUS_URL', 'https://prom.example.test:9090');
		mocks.listNodes.mockResolvedValue([]);
		mocks.readAcceleratorUse.mockRejectedValue(new Error('HTTP 503'));
		const body = await (await call()).json();
		expect(body.use.available).toBe(false);
		expect(body.use.reason).toContain('HTTP 503');
	});

	it('refuses a caller who may not list nodes', async () => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_PROMETHEUS_URL', 'https://prom.example.test:9090');
		mocks.listNodes.mockRejectedValue({ code: 403 });
		expect((await call()).status).toBe(403);
		expect(mocks.readAcceleratorUse).not.toHaveBeenCalled();
	});
});
