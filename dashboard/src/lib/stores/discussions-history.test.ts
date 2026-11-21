// How the Discussions page loads history: a small newest window, one thread by its own
// subject when it is opened, and older pages on demand.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';

const T1 = 'kubemoot.discuss.team.pilot.general.t1';
const T2 = 'kubemoot.discuss.team.pilot.general.t2';

function entry(seq: number, subject: string, threadId: string, messageType: string, content = 'x') {
	const data = JSON.stringify({
		messageId: `m-${seq}`,
		threadId,
		agentName: 'coordinator',
		messageType,
		content,
		channel: 'general',
		timestamp: new Date(Date.UTC(2026, 0, 1, 0, 0, seq)).toISOString()
	});
	return { seq, subject, data };
}

type Reply = { messages: ReturnType<typeof entry>[]; lastSeq?: number; windowStart?: number; hasMore?: boolean };

/** A fetch that answers by what was asked; every URL asked is kept. */
function fakeFetch(answer: (url: URL) => Reply) {
	const asked: URL[] = [];
	const fn = vi.fn(async (input: string) => {
		const url = new URL(input, 'https://dashboard');
		asked.push(url);
		if (url.pathname.endsWith('/stream')) return new Response('', { status: 404 });
		return Response.json(answer(url));
	});
	vi.stubGlobal('fetch', fn);
	vi.stubGlobal('EventSource', class { onopen = null; onmessage = null; onerror = null; close() {} });
	return asked;
}

async function fresh() {
	vi.resetModules();
	return import('./discussions');
}

const flush = () => new Promise((r) => setTimeout(r, 0));
const historyCalls = (asked: URL[]) => asked.filter((u) => u.pathname.endsWith('/history'));

beforeEach(() => vi.spyOn(console, 'error').mockImplementation(() => {}));
afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe('the first load', () => {
	it('reads the newest window of the stream, never the old 1000 messages', async () => {
		const asked = fakeFetch(() => ({ messages: [entry(900, T1, 't1', 'thread_start', 'question')], lastSeq: 900, windowStart: 801, hasMore: true }));
		const store = await fresh();
		store.initDiscussions();
		await flush();
		const first = historyCalls(asked)[0];
		expect(first.searchParams.get('subject')).toBe('kubemoot.discuss.>');
		expect(first.searchParams.get('limit')).toBe(String(store.HISTORY_PAGE));
		expect(store.HISTORY_PAGE).toBeLessThan(1000);
		expect(get(store.hasOlder)).toBe(true);
	});

	it('builds the thread list from the window', async () => {
		fakeFetch(() => ({
			messages: [entry(10, T1, 't1', 'thread_start', 'what is up'), entry(11, T1, 't1', 'agree'), entry(12, T2, 't2', 'thread_start', 'other')],
			lastSeq: 12,
			windowStart: 1,
			hasMore: false
		}));
		const store = await fresh();
		store.initDiscussions();
		await flush();
		const threads = get(store.sortedThreads);
		expect(threads.map((t) => t.threadId).sort((a, b) => a.localeCompare(b))).toEqual(['t1', 't2']);
		expect(threads.find((t) => t.threadId === 't1')?.agreeCount).toBe(1);
		expect(get(store.hasOlder)).toBe(false);
	});
});

describe('opening a thread', () => {
	// The window starts mid-thread: only a later message of t1 is in it.
	const answer = (url: URL): Reply =>
		url.searchParams.get('subject') === T1
			? {
					messages: [entry(100, T1, 't1', 'thread_start', 'the question'), entry(101, T1, 't1', 'agree'), entry(950, T1, 't1', 'synthesis')]
				}
			: { messages: [entry(950, T1, 't1', 'synthesis')], lastSeq: 950, windowStart: 751, hasMore: true };

	it('reads that thread by its own subject, once', async () => {
		const asked = fakeFetch(answer);
		const store = await fresh();
		store.initDiscussions();
		await flush();
		await store.ensureThreadLoaded('t1');
		await store.ensureThreadLoaded('t1');
		const threadReads = historyCalls(asked).filter((u) => u.searchParams.get('subject') === T1);
		expect(threadReads).toHaveLength(1);
		expect(threadReads[0].searchParams.has('before')).toBe(false);
	});

	it('shows the thread whole, in order, with its own question as the title', async () => {
		fakeFetch(answer);
		const store = await fresh();
		store.initDiscussions();
		await flush();
		await store.ensureThreadLoaded('t1');
		const thread = get(store.sortedThreads).find((t) => t.threadId === 't1');
		expect(thread?.userQuery).toBe('the question');
		expect(thread?.messages.map((m) => m.messageType)).toEqual(['thread_start', 'agree', 'synthesis']);
		expect(thread?.status).toBe('synthesized');
	});

	it('does not read a thread that is already complete', async () => {
		const asked = fakeFetch(() => ({ messages: [entry(10, T1, 't1', 'thread_start', 'q')], lastSeq: 10, windowStart: 1, hasMore: false }));
		const store = await fresh();
		store.initDiscussions();
		await flush();
		await store.ensureThreadLoaded('t1');
		expect(historyCalls(asked)).toHaveLength(1);
	});

	it('does nothing for a thread it has never seen', async () => {
		const asked = fakeFetch(() => ({ messages: [], lastSeq: 1, windowStart: 1, hasMore: false }));
		const store = await fresh();
		await store.ensureThreadLoaded('nope');
		expect(asked).toEqual([]);
	});

	it('keeps a message that was already shown while the read was in flight', async () => {
		fakeFetch(answer);
		const store = await fresh();
		store.initDiscussions();
		await flush();
		store.handleMessage(JSON.parse(entry(960, T1, 't1', 'follow_up').data), T1);
		await store.ensureThreadLoaded('t1');
		const types = get(store.sortedThreads).find((t) => t.threadId === 't1')?.messages.map((m) => m.messageType);
		expect(types).toEqual(['thread_start', 'agree', 'synthesis', 'follow_up']);
	});
});

describe('merging a thread read', () => {
	it('does not double a message that has no id, and orders one with no time last', async () => {
		const noId = (seq: number, type: string, stamp?: string) => {
			const e = entry(seq, T1, 't1', type);
			const d = JSON.parse(e.data);
			delete d.messageId;
			if (stamp === undefined) delete d.timestamp;
			return { ...e, data: JSON.stringify(d) };
		};
		const start = entry(100, T1, 't1', 'thread_start', 'q');
		const agree = noId(101, 'agree', 'x');
		fakeFetch((url) =>
			url.searchParams.get('subject') === T1
				? { messages: [start, agree, noId(102, 'concern')] }
				: { messages: [agree], lastSeq: 950, windowStart: 751, hasMore: false }
		);
		const store = await fresh();
		store.initDiscussions();
		await flush();
		await store.ensureThreadLoaded('t1');
		const types = get(store.sortedThreads).find((t) => t.threadId === 't1')?.messages.map((m) => m.messageType);
		expect(types).toEqual(['thread_start', 'agree', 'concern']);
	});
});

describe('older history', () => {
	it('pages back from where the loaded window starts', async () => {
		const asked = fakeFetch((url) =>
			url.searchParams.has('before')
				? { messages: [entry(300, T2, 't2', 'thread_start', 'older')], windowStart: 1, hasMore: false }
				: { messages: [entry(900, T1, 't1', 'thread_start', 'newer')], lastSeq: 900, windowStart: 701, hasMore: true }
		);
		const store = await fresh();
		store.initDiscussions();
		await flush();
		await store.loadOlder();
		const older = historyCalls(asked).find((u) => u.searchParams.has('before'));
		expect(older?.searchParams.get('before')).toBe('701');
		expect(get(store.sortedThreads).map((t) => t.threadId).sort((a, b) => a.localeCompare(b))).toEqual(['t1', 't2']);
		expect(get(store.hasOlder)).toBe(false);
	});

	it('does not read when nothing older exists', async () => {
		const asked = fakeFetch(() => ({ messages: [entry(5, T1, 't1', 'thread_start', 'q')], lastSeq: 5, windowStart: 1, hasMore: false }));
		const store = await fresh();
		store.initDiscussions();
		await flush();
		await store.loadOlder();
		expect(historyCalls(asked)).toHaveLength(1);
	});
});
