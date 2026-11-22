// A namespace-scoped dashboard drops other namespaces' messages after the fetch. A page it
// answers must hold messages or end paging; it never says "more" and returns nothing.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ getNatsConnection: vi.fn(), listNamespace: vi.fn() }));
vi.mock('#lib/server/nats-client.js', () => ({
	getNatsConnection: mocks.getNatsConnection,
	sc: { decode: (b: Uint8Array) => new TextDecoder().decode(b) }
}));
vi.mock('#lib/server/k8s/client.js', () => ({ getCoreApi: () => ({ listNamespace: mocks.listNamespace }), getServiceAccountCoreApi: () => ({ listNamespace: mocks.listNamespace }) }));

import { GET } from './history/+server';
import { resetScopeCache } from '#lib/server/scope.js';
import { resetHistoryReads } from '#lib/server/history-timing.js';

const enc = new TextEncoder();
const subjectOf = (ns: string, seq: number) => `kubemoot.discuss.${ns}.pilot.general.t${seq}`;

type Stored = { seq: number; subject: string };

/** What a fetch yields: the messages from `from` on, as JetStream messages. */
async function* deliver(all: Stored[], from: number) {
	for (const m of all.filter((x) => x.seq >= from)) {
		yield { seq: m.seq, subject: m.subject, data: enc.encode(JSON.stringify({ threadId: `t${m.seq}`, messageType: 'agree' })) };
	}
}

/** A stream of `last` messages in which only the sequences in `mine` belong to namespace "team". */
function sparseStream(last: number, mine: number[]) {
	const asked: number[] = [];
	const all = Array.from({ length: last }, (_, i) => i + 1).map((seq) => ({ seq, subject: subjectOf(mine.includes(seq) ? 'team' : 'other', seq) }));
	let from = 1;
	const nc = {
		jetstreamManager: async () => ({
			streams: { info: async () => ({ state: { first_seq: 1, last_seq: last, messages: last } }) },
			consumers: {
				add: async (_s: string, cfg: { opt_start_seq?: number }) => {
					from = cfg.opt_start_seq ?? 1;
					asked.push(from);
					return { name: 'c', num_pending: all.filter((m) => m.seq >= from).length };
				},
				delete: async () => true
			}
		}),
		jetstream: () => ({
			consumers: {
				get: async () => ({
					fetch: async () => deliver(all, from)
				})
			}
		})
	};
	return { nc, asked };
}

const call = (query: string) => GET({ url: new URL(`https://dashboard/api/nats/history?${query}`) } as never);

beforeEach(() => {
	vi.stubEnv('KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR', 'kubemoot.ai/workshop-team=true');
	mocks.listNamespace.mockResolvedValue({ items: [{ metadata: { name: 'team' } }] });
	mocks.getNatsConnection.mockReset();
	resetScopeCache();
	resetHistoryReads();
	vi.spyOn(console, 'info').mockImplementation(() => {});
});
afterEach(() => {
	vi.unstubAllEnvs();
	vi.restoreAllMocks();
});

describe('paging on a namespace-scoped dashboard', () => {
	it('skips windows with nothing it may show and returns the first page that has messages', async () => {
		const { nc, asked } = sparseStream(5000, [10, 20]);
		mocks.getNatsConnection.mockResolvedValue(nc);
		const body = await (await call('limit=200')).json();
		expect(body.messages.map((m: { seq: number }) => m.seq)).toEqual([10, 20]);
		expect(asked.length).toBeGreaterThan(1);
		expect(body.hasMore).toBe(false);
	});

	it('answers a page that has messages with hasMore true while older ones remain', async () => {
		const { nc } = sparseStream(5000, [4900, 20]);
		mocks.getNatsConnection.mockResolvedValue(nc);
		const body = await (await call('limit=200')).json();
		expect(body.messages.map((m: { seq: number }) => m.seq)).toEqual([4900]);
		expect(body.hasMore).toBe(true);
		const older = await (await call(`limit=200&before=${body.windowStart}`)).json();
		expect(older.messages.map((m: { seq: number }) => m.seq)).toEqual([20]);
		expect(older.hasMore).toBe(false);
	});

	it('ends paging, with no messages, when none of the older messages are the namespace\'s', async () => {
		const { nc } = sparseStream(3000, []);
		mocks.getNatsConnection.mockResolvedValue(nc);
		const body = await (await call('limit=200')).json();
		expect(body.messages).toEqual([]);
		expect(body.hasMore).toBe(false);
	});
});
