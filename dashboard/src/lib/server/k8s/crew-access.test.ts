// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
	items: [] as { metadata?: { namespace?: string } }[],
	keys: [] as string[],
	saFails: false,
	busFails: false,
	incomplete: false,
	discovery: false,
	review: async (_ns: string): Promise<unknown> => ({ status: { allowed: true } }),
	inFlight: 0,
	maxInFlight: 0
}));
vi.mock('../mode.js', () => ({ crewNamespaceDiscovery: () => mocks.discovery }));
vi.mock('../nats-client.js', () => ({
	getNatsConnection: async () => {
		if (mocks.busFails) throw new Error('nats down');
		return { jetstream: () => ({ views: { kv: async () => ({}) } }) };
	}
}));
vi.mock('../kv-snapshot.js', () => ({
	readKvSnapshot: async (_kv: unknown, pick: (e: { key: string; operation: string }) => unknown) => ({
		values: Object.fromEntries(mocks.keys.map((key, i) => [`k${i}`, pick({ key, operation: key.startsWith('!') ? 'DEL' : 'PUT' })])),
		complete: !mocks.incomplete
	})
}));
vi.mock('./client.js', () => ({
	getServiceAccountCustomObjectsApi: () => ({
		listClusterCustomObject: async () => {
			if (mocks.saFails) throw new Error('forbidden');
			return { items: mocks.items };
		}
	}),
	getAuthorizationApi: () => ({
		createSelfSubjectAccessReview: async ({ body }: { body: { spec: { resourceAttributes: { namespace: string } } } }) => {
			mocks.inFlight++;
			mocks.maxInFlight = Math.max(mocks.maxInFlight, mocks.inFlight);
			try {
				return await mocks.review(body.spec.resourceAttributes.namespace);
			} finally {
				mocks.inFlight--;
			}
		}
	})
}));

import { REVIEW_CONCURRENCY, crewNamespaces, mayGetCrews, natsNamespaces, readableNamespaces } from './crew-access';

const byName = (a: string, b: string) => a.localeCompare(b);

beforeEach(() => {
	mocks.items = [];
	mocks.keys = [];
	mocks.saFails = false;
	mocks.busFails = false;
	mocks.incomplete = false;
	mocks.discovery = false;
	mocks.review = async () => ({ status: { allowed: true } });
	mocks.inFlight = 0;
	mocks.maxInFlight = 0;
});

describe('natsNamespaces', () => {
	it('is the namespace of each live heartbeat key, once', async () => {
		mocks.keys = ['a.agent-1', 'a.agent-2', 'b.agent-3', '!c.gone', '.odd'];
		expect((await natsNamespaces()).sort(byName)).toEqual(['a', 'b']);
	});
});

describe('an incomplete read of the bus', () => {
	it('is said in the log, and what was read still counts', async () => {
		const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
		mocks.incomplete = true;
		mocks.keys = ['a.agent'];
		expect(await natsNamespaces()).toEqual(['a']);
		expect(warn).toHaveBeenCalledWith(expect.stringContaining('not fully read'));
		warn.mockRestore();
	});
});

describe('crewNamespaces', () => {
	it('reads the bus only, never the Crew list, unless the chart granted it', async () => {
		mocks.keys = ['a.agent'];
		mocks.items = [{ metadata: { namespace: 'listed' } }];
		expect(await crewNamespaces()).toEqual(['a']);
	});

	it('adds each namespace that holds a Crew once, skipping items with none, when granted', async () => {
		mocks.discovery = true;
		mocks.keys = ['a.agent'];
		mocks.items = [{ metadata: { namespace: 'a' } }, { metadata: { namespace: 'b' } }, { metadata: { namespace: 'b' } }, {}, { metadata: {} }];
		expect((await crewNamespaces()).sort(byName)).toEqual(['a', 'b']);
	});

	it('keeps what one source found when the other fails, and is empty when both do', async () => {
		mocks.discovery = true;
		mocks.keys = ['a.agent'];
		mocks.saFails = true;
		expect(await crewNamespaces()).toEqual(['a']);
		mocks.busFails = true;
		expect(await crewNamespaces()).toEqual([]);
	});
});

describe('mayGetCrews', () => {
	it('is true only for a plain yes', async () => {
		expect(await mayGetCrews('a')).toBe(true);
		for (const answer of [{ status: { allowed: false } }, { status: {} }, {}, { status: { allowed: 'true' } }]) {
			mocks.review = async () => answer;
			expect(await mayGetCrews('a')).toBe(false);
		}
	});

	it('is false when the review fails', async () => {
		mocks.review = async () => {
			throw new Error('401');
		};
		expect(await mayGetCrews('a')).toBe(false);
	});
});

describe('readableNamespaces', () => {
	it('keeps the namespaces reviewed as allowed, and drops one whose review failed', async () => {
		mocks.review = async (ns) => {
			if (ns === 'broken') throw new Error('boom');
			return { status: { allowed: ns !== 'denied' } };
		};
		expect(await readableNamespaces(['ok', 'denied', 'broken'])).toEqual(new Set(['ok']));
	});

	it('reviews only a few at a time', async () => {
		mocks.review = async () => {
			await new Promise((r) => setTimeout(r, 1));
			return { status: { allowed: true } };
		};
		const names = Array.from({ length: REVIEW_CONCURRENCY * 3 }, (_, i) => `ns-${i}`);
		expect((await readableNamespaces(names)).size).toBe(names.length);
		expect(mocks.maxInFlight).toBeLessThanOrEqual(REVIEW_CONCURRENCY);
	});

	it('reviews nothing for no namespaces', async () => {
		expect(await readableNamespaces([])).toEqual(new Set());
	});
});
