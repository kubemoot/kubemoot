import { AckPolicy, DeliverPolicy } from 'nats';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ getNatsConnection: vi.fn() }));
vi.mock('#lib/server/nats-client.js', () => ({
	getNatsConnection: mocks.getNatsConnection,
	sc: { decode: (b: Uint8Array) => new TextDecoder().decode(b) }
}));

import { GET } from './history/+server';
import { isLiteralSubject, parseParams, startPlan } from '#lib/server/history-read.js';
import { historyReadSummary, resetHistoryReads } from '#lib/server/history-timing.js';

const enc = new TextEncoder();
const THREAD = 'kubemoot.discuss.team.pilot.general.t-42';

/** A stream of `last` messages; `matching` of them (by seq) are on the asked subject. */
function fakeStream(opts: { first?: number; last: number; pending?: number; seqs?: number[] }) {
	const added: Record<string, unknown>[] = [];
	const fetchOpts: Record<string, unknown>[] = [];
	const seqs = opts.seqs ?? [];
	const nc = {
		jetstreamManager: async () => ({
			streams: { info: async () => ({ state: { first_seq: opts.first ?? 1, last_seq: opts.last, messages: opts.last } }) },
			consumers: {
				add: async (_s: string, cfg: Record<string, unknown>) => {
					added.push(cfg);
					return { name: 'ephemeral', num_pending: opts.pending ?? seqs.length };
				},
				delete: async () => true
			}
		}),
		jetstream: () => ({
			consumers: {
				get: async () => ({
					fetch: async (o: Record<string, unknown>) => {
						fetchOpts.push(o);
						return {
							async *[Symbol.asyncIterator]() {
								for (const seq of seqs) {
									yield { seq, subject: THREAD, data: enc.encode(JSON.stringify({ threadId: 't-42', messageType: 'agree', n: seq })) };
								}
							}
						};
					}
				})
			}
		})
	};
	return { nc, added, fetchOpts };
}

const call = (query: string) => GET({ url: new URL(`https://dashboard/api/nats/history?${query}`) } as never);

beforeEach(() => {
	mocks.getNatsConnection.mockReset();
	resetHistoryReads();
	vi.spyOn(console, 'info').mockImplementation(() => {});
});
afterEach(() => vi.restoreAllMocks());

describe('history parameters', () => {
	it('defaults to the discussion stream, every subject, 500 messages and no upper bound', () => {
		expect(parseParams(new URL('https://d/x'))).toEqual({
			stream: 'KUBEMOOT_DISCUSS',
			subject: 'kubemoot.discuss.>',
			limit: 500,
			before: Number.POSITIVE_INFINITY
		});
	});

	it('caps the limit and ignores a limit or before that is not a positive number', () => {
		expect(parseParams(new URL('https://d/x?limit=99999')).limit).toBe(2000);
		expect(parseParams(new URL('https://d/x?limit=abc&before=-4'))).toMatchObject({ limit: 500, before: Number.POSITIVE_INFINITY });
	});

	it('tells a thread from a pattern', () => {
		expect(isLiteralSubject(THREAD)).toBe(true);
		expect(isLiteralSubject('kubemoot.discuss.>')).toBe(false);
		expect(isLiteralSubject('kubemoot.discuss.*.pilot.>')).toBe(false);
	});
});

describe('where a read starts', () => {
	const p = (over: object) => ({ stream: 's', subject: 'kubemoot.discuss.>', limit: 200, before: Infinity, ...over });

	it('reads a thread whole from the start of the stream', () => {
		expect(startPlan(p({ subject: THREAD }), 5, 9000)).toEqual({ policy: DeliverPolicy.All, startSeq: 5 });
	});

	it('reads the newest window of a pattern', () => {
		expect(startPlan(p({}), 1, 1000)).toEqual({ policy: DeliverPolicy.StartSequence, startSeq: 801 });
	});

	it('pages back from before', () => {
		expect(startPlan(p({ before: 801 }), 1, 1000)).toEqual({ policy: DeliverPolicy.StartSequence, startSeq: 601 });
	});

	it('does not start below the first sequence', () => {
		expect(startPlan(p({ before: 50 }), 30, 1000).startSeq).toBe(30);
	});
});

describe('GET /api/nats/history', () => {
	it('reads one thread by its own subject, not the stream', async () => {
		const { nc, added } = fakeStream({ last: 5000, seqs: [100, 140, 190] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		const res = await call(`subject=${encodeURIComponent(THREAD)}&limit=500`);
		const body = await res.json();
		expect(body.messages.map((m: { seq: number }) => m.seq)).toEqual([100, 140, 190]);
		expect(added[0]).toMatchObject({ ack_policy: AckPolicy.None, deliver_policy: DeliverPolicy.All, filter_subject: THREAD });
		expect(body.hasMore).toBe(false);
	});

	it('says a thread read was cut short when the thread holds more than the limit', async () => {
		const { nc } = fakeStream({ last: 5000, pending: 9, seqs: [1, 2] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		const body = await (await call(`subject=${encodeURIComponent(THREAD)}&limit=5`)).json();
		expect(body.truncated).toBe(true);
	});

	it('asks for no more messages than the consumer holds, so the fetch does not wait out its expiry', async () => {
		const { nc, fetchOpts } = fakeStream({ last: 5000, seqs: [1, 2, 3] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		await call(`subject=${encodeURIComponent(THREAD)}&limit=500`);
		expect(fetchOpts[0].max_messages).toBe(3);
	});

	it('reads the newest window of the stream and tells where an older page starts', async () => {
		const { nc, added } = fakeStream({ last: 1000, pending: 200, seqs: [801, 802] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		const body = await (await call('limit=200')).json();
		expect(added[0]).toMatchObject({ deliver_policy: DeliverPolicy.StartSequence, opt_start_seq: 801 });
		expect(body).toMatchObject({ lastSeq: 1000, windowStart: 801, hasMore: true });
	});

	it('stops at the sequence passed as before', async () => {
		const { nc } = fakeStream({ last: 1000, pending: 400, seqs: [600, 700, 800, 801, 900] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		const body = await (await call('limit=200&before=801')).json();
		expect(body.messages.map((m: { seq: number }) => m.seq)).toEqual([600, 700, 800]);
	});

	it('says there is nothing older once the window reaches the first message', async () => {
		const { nc } = fakeStream({ last: 150, seqs: [1] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		expect((await (await call('limit=200')).json()).hasMore).toBe(false);
	});

	it('answers an empty stream without a consumer', async () => {
		const { nc, added } = fakeStream({ last: 0 });
		mocks.getNatsConnection.mockResolvedValue(nc);
		expect(await (await call('limit=10')).json()).toEqual({ messages: [], lastSeq: 0 });
		expect(added).toEqual([]);
	});

	it('reports its own timing: a Server-Timing header, the summary, and a content-free log line', async () => {
		const { nc } = fakeStream({ last: 1000, pending: 2, seqs: [900, 901] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		const res = await call('limit=200');
		expect(res.headers.get('Server-Timing')).toMatch(/^setup;dur=\d+, fetch;dur=\d+, total;dur=\d+$/);
		const summary = historyReadSummary();
		expect(summary).toMatchObject({ count: 1, lastMessages: 2 });
		expect(summary?.lastBytes).toBe(Buffer.byteLength(await res.text()));
		const line = vi.mocked(console.info).mock.calls[0][0] as string;
		expect(line).toMatch(/^nats-history scope=stream messages=2 bytes=\d+ setup_ms=\d+ fetch_ms=\d+ total_ms=\d+$/);
		expect(line).not.toContain('t-42');
		expect(line).not.toContain('team');
	});

	it('logs a thread read as scope=thread', async () => {
		const { nc } = fakeStream({ last: 1000, seqs: [5] });
		mocks.getNatsConnection.mockResolvedValue(nc);
		await call(`subject=${encodeURIComponent(THREAD)}`);
		expect(vi.mocked(console.info).mock.calls[0][0]).toContain('scope=thread');
	});

	it('answers 500 with the reason when NATS fails', async () => {
		mocks.getNatsConnection.mockRejectedValue(new Error('down'));
		const res = await call('limit=10');
		expect(res.status).toBe(500);
		expect(await res.json()).toMatchObject({ error: 'down' });
	});
});
