import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ readNatsStatus: vi.fn(), readCrewUsage: vi.fn(), listCrews: vi.fn() }));
vi.mock('#lib/server/nats-status.js', () => ({ readNatsStatus: mocks.readNatsStatus, readCrewUsage: mocks.readCrewUsage }));
vi.mock('#lib/server/k8s/index.js', () => ({ listCrews: mocks.listCrews }));
vi.mock('#lib/server/k8s/client.js', () => ({ getCoreApi: () => ({}) }));

describe('NATS status routes', () => {
	beforeEach(() => {
		vi.resetModules();
		for (const m of Object.values(mocks)) m.mockReset();
	});

	it('GET /api/nats/status answers what the read returns, uncached by the browser', async () => {
		mocks.readNatsStatus.mockResolvedValue({ connected: true, stores: [], absent: [], checkedAt: 'now' });
		const { GET } = await import('./status/+server');
		const res = await (GET as unknown as () => Promise<Response>)();
		expect(res.headers.get('Cache-Control')).toBe('no-store');
		expect(await res.json()).toMatchObject({ connected: true });
	});

	it('shares one read between requests inside the interval', async () => {
		mocks.readNatsStatus.mockResolvedValue({ connected: true, stores: [], absent: [], checkedAt: 'now' });
		const { GET } = await import('./status/+server');
		await (GET as unknown as () => Promise<Response>)();
		await (GET as unknown as () => Promise<Response>)();
		expect(mocks.readNatsStatus).toHaveBeenCalledTimes(1);
	});

	it('GET /api/nats/crews adds each crew\'s last retention cleanup to its usage', async () => {
		mocks.listCrews.mockResolvedValue({
			items: [
				{ metadata: { namespace: 'team', name: 'pilot' }, status: { retention: { lastRunTime: 't', removed: { memory: 2 } } } },
				{ metadata: { namespace: 'team', name: 'new' } }
			]
		});
		mocks.readCrewUsage.mockResolvedValue([
			{ namespace: 'team', crew: 'pilot', stores: [] },
			{ namespace: 'team', crew: 'new', stores: [] }
		]);
		const { GET } = await import('./crews/+server');
		const body = await (await (GET as unknown as () => Promise<Response>)()).json();
		expect(mocks.readCrewUsage).toHaveBeenCalledWith([
			{ namespace: 'team', crew: 'pilot' },
			{ namespace: 'team', crew: 'new' }
		]);
		expect(body.cap).toBeGreaterThan(0);
		expect(body.crews[0].retention).toEqual({ lastRunTime: 't', removed: { memory: 2 } });
		expect(body.crews[1].retention).toBeNull();
	});

	it('answers 500 with the reason when NATS cannot be read', async () => {
		mocks.listCrews.mockResolvedValue({ items: [] });
		mocks.readCrewUsage.mockRejectedValue(new Error('NATS connection timed out'));
		const { GET } = await import('./crews/+server');
		const res = await (GET as unknown as () => Promise<Response>)();
		expect(res.status).toBe(500);
		expect(await res.json()).toMatchObject({ error: 'NATS connection timed out', crews: [] });
	});
});
