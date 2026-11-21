/* eslint-disable sonarjs/no-clear-text-protocols -- fixtures: the NATS monitoring port is an in-cluster plain HTTP endpoint */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ getNatsConnection: vi.fn() }));
vi.mock('./nats-client.js', () => ({
	getNatsConnection: mocks.getNatsConnection,
	sc: { encode: (s: string) => new TextEncoder().encode(s), decode: (b: Uint8Array) => new TextDecoder().decode(b) }
}));

import {
	absentStores,
	inBatches,
	listObjectsBounded,
	monitorUrl,
	objectsOfCrew,
	readCrewUsage,
	readNatsStatus,
	storeStatusOf,
	storeVisible
} from './nats-status';
import { CREW_ENTRY_CAP } from '#lib/nats/crew-usage.js';
import { STORES } from '#lib/nats/stores.js';

const NS = 1_000_000_000;

function streamInfo(name: string, over: Record<string, unknown> = {}, state: Record<string, unknown> = {}) {
	return {
		config: { name, max_age: 24 * 3600 * NS, max_bytes: 1000, max_msgs: 100, max_msgs_per_subject: -1, ...over },
		state: { messages: 10, bytes: 100, first_seq: 1, last_seq: 10, consumer_count: 2, ...state }
	};
}

describe('storeStatusOf', () => {
	it('reads a stream with its definition and limits', () => {
		const s = storeStatusOf(streamInfo('KUBEMOOT_DISCUSS') as never);
		expect(s.definition?.name).toBe('KUBEMOOT_DISCUSS');
		expect(s.kind).toBe('stream');
		expect(s.limits).toEqual({ maxAgeSeconds: 86400, maxBytes: 1000, maxMessages: 100, history: null });
		expect(s.usage).toMatchObject({ messages: 10, bytes: 100, firstSeq: 1, lastSeq: 10, consumers: 2, keys: null });
	});

	it('shows a KV bucket under its bucket name with its history', () => {
		const s = storeStatusOf(streamInfo('KV_kubemoot_crew_memory', { max_msgs_per_subject: 5, max_age: 0 }, { num_subjects: 3 }) as never);
		expect(s.name).toBe('kubemoot_crew_memory');
		expect(s.kind).toBe('kv');
		expect(s.limits.history).toBe(5);
		expect(s.limits.maxAgeSeconds).toBeNull();
		expect(s.usage.keys).toBe(3);
	});

	it('shows an object store under its name, and a limit of -1 as none', () => {
		const s = storeStatusOf(streamInfo('OBJ_kubemoot_workspace', { max_bytes: -1, max_msgs: -1 }) as never);
		expect([s.name, s.kind, s.limits.maxBytes, s.limits.maxMessages]).toEqual(['kubemoot_workspace', 'object', null, null]);
	});

	it('keeps a stream Kubemoot does not define, without a definition', () => {
		const s = storeStatusOf(streamInfo('SOMEONE_ELSES') as never);
		expect(s.definition).toBeNull();
		expect(s.name).toBe('SOMEONE_ELSES');
	});
});

describe('absentStores', () => {
	it('lists the defined stores the server does not hold', () => {
		const held = STORES.filter((s) => s.name !== 'kubemoot_coordinator_leases').map((s) => ({ name: s.name }));
		expect(absentStores(held as never)).toEqual(['kubemoot_coordinator_leases']);
	});
});

describe('storeVisible', () => {
	it('shows every store on an unscoped dashboard', () => {
		expect(storeVisible({ name: 'kubemoot_crew_memory', kind: 'kv' })).toBe(true);
	});
});

describe('monitorUrl', () => {
	it('prefers the setting', () => expect(monitorUrl('nats://n:4222', 'http://m:9/varz')).toBe('http://m:9/varz'));
	it('derives the monitoring port from the NATS host', () => {
		expect(monitorUrl('nats://nats.nats.svc.cluster.local:4222', undefined)).toBe('http://nats.nats.svc.cluster.local:8222/varz');
		expect(monitorUrl('nats://a:4222,nats://b:4222', undefined)).toBe('http://a:8222/varz');
	});
	it('is null without a usable address', () => {
		expect(monitorUrl(undefined, undefined)).toBeNull();
		expect(monitorUrl('not a url', undefined)).toBeNull();
	});
});

describe('inBatches', () => {
	it('runs every task, in order of the results, with a bound on how many run at once', async () => {
		let running = 0;
		let peak = 0;
		const tasks = Array.from({ length: 10 }, (_, i) => async () => {
			running++;
			peak = Math.max(peak, running);
			await new Promise((r) => setTimeout(r, 2));
			running--;
			return i;
		});
		expect(await inBatches(tasks, 3)).toEqual([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]);
		expect(peak).toBeLessThanOrEqual(3);
	});

	it('does nothing for no tasks', async () => expect(await inBatches([], 4)).toEqual([]));
});

describe('objectsOfCrew', () => {
	it('keeps the objects under the crew prefix, and no other crew with a similar name', () => {
		const entries = [
			{ name: 'ns/pilot/t1/a', size: 1 },
			{ name: 'ns/pilot-two/t1/a', size: 1 },
			{ name: 'other/pilot/t1/a', size: 1 }
		];
		expect(objectsOfCrew(entries, 'ns/pilot/')).toEqual([entries[0]]);
	});

	it('stops at the cap', () => {
		const entries = Array.from({ length: CREW_ENTRY_CAP + 10 }, (_, i) => ({ name: `ns/c/${i}`, size: 1 }));
		expect(objectsOfCrew(entries, 'ns/c/')).toHaveLength(CREW_ENTRY_CAP);
	});
});

function fakeConnection(opts: { streams?: unknown[]; subjects?: Record<string, number>; total?: number; objects?: { name: string; size: number }[] } = {}) {
	const request = vi.fn(async () => ({
		json: () => ({ total: opts.total ?? Object.keys(opts.subjects ?? {}).length, state: { subjects: opts.subjects ?? {} } })
	}));
	const watch = {
		stop: vi.fn(),
		async *[Symbol.asyncIterator]() {
			yield* opts.objects ?? [];
			yield null;
		}
	};
	const nc = {
		info: { version: '2.12.0', server_name: 'nats-0' },
		request,
		jetstream: () => ({ views: { os: async () => ({ watch: async () => watch }) } }),
		jetstreamManager: async () => ({
			getAccountInfo: async () => ({
				storage: 500,
				memory: 0,
				streams: 2,
				consumers: 1,
				limits: { max_storage: 1000, max_memory: -1, max_streams: -1, max_consumers: -1 }
			}),
			streams: {
				info: async () => ({}),
				list: () => ({
					async *[Symbol.asyncIterator]() {
						yield* opts.streams ?? [];
					}
				})
			}
		})
	};
	return { nc, request };
}

beforeEach(() => {
	mocks.getNatsConnection.mockReset();
	vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 404 })));
});

describe('readNatsStatus', () => {
	it('reads the server, the account limits and every store', async () => {
		const { nc } = fakeConnection({ streams: [streamInfo('KV_kubemoot_agent_state'), streamInfo('KUBEMOOT_CHAT')] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		const status = await readNatsStatus();
		expect(status.connected).toBe(true);
		expect(status.server).toMatchObject({ version: '2.12.0', name: 'nats-0', uptime: null });
		expect(status.account?.storage).toEqual({ used: 500, limit: 1000 });
		expect(status.account?.memory).toEqual({ used: 0, limit: null });
		expect(status.stores.map((s) => s.name)).toEqual(['kubemoot_agent_state', 'KUBEMOOT_CHAT']);
		expect(status.absent).toContain('KUBEMOOT_DISCUSS');
	});

	it('answers disconnected, with the reason, when NATS cannot be reached', async () => {
		mocks.getNatsConnection.mockRejectedValue(new Error('connection refused'));
		const status = await readNatsStatus();
		expect(status).toMatchObject({ connected: false, error: 'connection refused', stores: [] });
	});

	it('takes uptime and connections from the monitoring port when it answers', async () => {
		const { nc } = fakeConnection();
		mocks.getNatsConnection.mockResolvedValue(nc);
		vi.stubGlobal('fetch', vi.fn(async () => Response.json({ uptime: '3d2h', connections: 12 })));
		vi.stubEnv('NATS_MONITOR_URL', 'http://nats:8222/varz');
		const status = await readNatsStatus();
		vi.unstubAllEnvs();
		expect(status.server).toMatchObject({ uptime: '3d2h', connections: 12 });
	});
});

describe('listObjectsBounded', () => {
	it('lists the objects', async () => {
		const { nc } = fakeConnection({ objects: [{ name: 'a/b/1', size: 5 }] });
		expect(await listObjectsBounded(nc as never, 'kubemoot_workspace', 10)).toEqual({ entries: [{ name: 'a/b/1', size: 5 }], capped: false });
	});

	it('stops at the cap and says so', async () => {
		const objects = Array.from({ length: 6 }, (_, i) => ({ name: `a/b/${i}`, size: 1 }));
		const { nc } = fakeConnection({ objects });
		const r = await listObjectsBounded(nc as never, 'kubemoot_workspace', 3);
		expect([r.entries.length, r.capped]).toEqual([3, true]);
	});
});

describe('readCrewUsage', () => {
	it('asks NATS for each crew\'s own subjects, never the whole bucket', async () => {
		const { nc, request } = fakeConnection({
			streams: [streamInfo('KV_kubemoot_crew_memory', {}, { messages: 100, bytes: 10_000 })],
			subjects: { '$KV.kubemoot_crew_memory.ns.pilot.t.k': 4 }
		});
		mocks.getNatsConnection.mockResolvedValue(nc);
		const [usage] = await readCrewUsage([{ namespace: 'ns', crew: 'pilot' }]);
		const calls = request.mock.calls as unknown as [string, Uint8Array][];
		const asked = calls.map(([subject, body]) => [subject, JSON.parse(new TextDecoder().decode(body))]);
		expect(asked).toContainEqual(['$JS.API.STREAM.INFO.KV_kubemoot_crew_memory', { subjects_filter: '$KV.kubemoot_crew_memory.ns.pilot.>' }]);
		expect(usage.stores).toEqual([
			expect.objectContaining({ store: 'kubemoot_crew_memory', messages: 4, keys: 1, bytes: 400, bytesEstimated: true })
		]);
	});

	it('leaves out a store that answers with an error', async () => {
		const { nc } = fakeConnection({ streams: [streamInfo('KV_kubemoot_crew_memory')] });
		nc.request.mockRejectedValue(new Error('timeout'));
		mocks.getNatsConnection.mockResolvedValue(nc);
		const [usage] = await readCrewUsage([{ namespace: 'ns', crew: 'pilot' }]);
		expect(usage.stores).toEqual([]);
	});

	it('sums a crew\'s objects exactly from one bounded listing', async () => {
		const { nc } = fakeConnection({
			streams: [streamInfo('OBJ_kubemoot_workspace')],
			objects: [{ name: 'ns/pilot/t/a', size: 10 }, { name: 'ns/pilot/t/b', size: 20 }, { name: 'ns/other/t/c', size: 99 }]
		});
		mocks.getNatsConnection.mockResolvedValue(nc);
		const [usage] = await readCrewUsage([{ namespace: 'ns', crew: 'pilot' }]);
		expect(usage.stores[0]).toMatchObject({ store: 'kubemoot_workspace', messages: 2, bytes: 30, bytesEstimated: false });
	});
});
