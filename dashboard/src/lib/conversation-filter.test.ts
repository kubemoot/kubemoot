import { describe, expect, it } from 'vitest';
import type { Thread } from '#lib/stores/discussions.js';
import {
	DEFAULT_FILTER,
	NO_FILTER,
	chipsOf,
	firstHit,
	emptyBecauseOfFilter,
	hasFilterParams,
	highlight,
	matchesFilter,
	parseFilter,
	snippet,
	threadState,
	withFilter,
	withoutChip
} from './conversation-filter';

const CID = '11111111-1111-4111-8111-111111111111';

function thread(over: Partial<Thread> = {}, messages: Thread['messages'] = []): Thread {
	return {
		threadId: 't1',
		channel: 'c',
		userQuery: 'Why is the GPU node hot?',
		startedBy: 'u',
		startedAt: '2026-11-10T12:00:00',
		messages: [{ messageType: 'thread_start', content: 'q', metadata: { conversationId: CID } } as never, ...messages],
		status: 'closed',
		...over
	} as Thread;
}

describe('the query string', () => {
	it('round trips every filter and keeps other parameters', () => {
		const f = { status: 'failed', mine: true, range: 'custom', from: '2026-11-01T09:00', to: '2026-11-21T17:30', q: 'gpu' } as const;
		const params = withFilter(new URLSearchParams('thread=abc'), f);
		expect(params.toString()).toBe('thread=abc&status=failed&mine=1&range=custom&from=2026-11-01T09%3A00&to=2026-11-21T17%3A30&q=gpu');
		expect(parseFilter(params)).toEqual(f);
	});

	it('has no range in the address for the default, and names Anytime and a preset', () => {
		expect(withFilter(new URLSearchParams(), DEFAULT_FILTER).toString()).toBe('');
		expect(withFilter(new URLSearchParams(), NO_FILTER).toString()).toBe('range=any');
		expect(withFilter(new URLSearchParams(), { ...NO_FILTER, range: '1h' }).toString()).toBe('range=1h');
		expect(parseFilter(new URLSearchParams('range=any')).range).toBe('any');
		expect(parseFilter(new URLSearchParams('range=1h')).range).toBe('1h');
	});

	it('shows the default range when the address says none, and drops what it cannot read', () => {
		expect(parseFilter(new URLSearchParams(''))).toEqual(DEFAULT_FILTER);
		expect(parseFilter(new URLSearchParams('status=bogus&range=lastcentury&from=yesterday&to=2026-13-45&mine=yes&q=%20%20'))).toEqual(DEFAULT_FILTER);
	});

	it('reads an older address that has only from and to as a custom range of whole days', () => {
		const f = parseFilter(new URLSearchParams('from=2026-11-01&to=2026-11-21'));
		expect(f).toMatchObject({ range: 'custom', from: '2026-11-01T00:00', to: '2026-11-21T23:59' });
	});

	it('ignores from and to unless the range is custom', () => {
		expect(parseFilter(new URLSearchParams('range=1h&from=2026-11-01T09:00'))).toMatchObject({ range: '1h', from: null, to: null });
	});

	it('removes a filter that is cleared', () => {
		expect(withFilter(new URLSearchParams('status=failed&q=x'), DEFAULT_FILTER).toString()).toBe('');
	});

	it('bounds the search text', () => {
		expect(parseFilter(new URLSearchParams({ q: 'x'.repeat(500) })).q).toHaveLength(200);
	});

	it('says whether the address sets any filter', () => {
		expect(hasFilterParams(new URLSearchParams('thread=abc'))).toBe(false);
		expect(hasFilterParams(new URLSearchParams('thread=abc&range=any'))).toBe(true);
		expect(hasFilterParams(new URLSearchParams('q=gpu'))).toBe(true);
	});
});

describe('chips', () => {
	it('lists one removable chip per filter set, the time range included', () => {
		const f = { status: 'active', mine: true, range: '6h', from: null, to: null, q: 'gpu' } as const;
		expect(chipsOf(f).map((c) => c.key)).toEqual(['status', 'mine', 'range', 'q']);
		expect(chipsOf(f).find((c) => c.key === 'range')?.label).toBe('Last 6 hours');
		expect(chipsOf(withoutChip(f, 'mine')).map((c) => c.key)).toEqual(['status', 'range', 'q']);
		expect(chipsOf(NO_FILTER)).toEqual([]);
	});

	it('removing the range chip means any time, and clears a custom range', () => {
		const custom = { ...NO_FILTER, range: 'custom', from: '2026-11-01T09:00', to: null } as const;
		expect(withoutChip(custom, 'range')).toEqual(NO_FILTER);
		expect(chipsOf(custom)[0].label).toMatch(/^From /);
	});

	it('shows no chip for a filter at its default, and one once the range is changed', () => {
		expect(chipsOf(DEFAULT_FILTER)).toEqual([]);
		expect(chipsOf({ ...DEFAULT_FILTER, range: '3d' }).map((c) => c.label)).toEqual(['Last 3 days']);
	});
});

describe('why an empty list is empty', () => {
	it('names the search, then the other filters, then the time range', () => {
		expect(emptyBecauseOfFilter({ ...DEFAULT_FILTER, q: 'zebra', status: 'failed' }, 'discussions')).toBe('No discussions match this search.');
		expect(emptyBecauseOfFilter({ ...DEFAULT_FILTER, mine: true }, 'conversations')).toBe('No conversations match these filters.');
		expect(emptyBecauseOfFilter(DEFAULT_FILTER, 'discussions')).toMatch(/^No discussions in this time range\. Choose Anytime/);
	});

	it('says nothing about filters when none is set, so the list can give its own reason', () => {
		expect(emptyBecauseOfFilter(NO_FILTER, 'conversations')).toBeNull();
	});
});

describe('status', () => {
	it('is active while open, closed when answered, failed on an error', () => {
		expect(threadState(thread({ status: 'open' }))).toBe('active');
		expect(threadState(thread({ status: 'synthesized' }))).toBe('closed');
		expect(threadState(thread({ status: 'closed' }, [{ messageType: 'error' } as never]))).toBe('failed');
		expect(threadState(thread({ status: 'closed' }, [{ messageType: 'synthesis', metadata: { failureType: 'tool' } } as never]))).toBe('failed');
	});
});

describe('matchesFilter', () => {
	const mine = new Set([CID]);

	it('passes everything with no filter', () => {
		expect(matchesFilter(thread(), NO_FILTER, null)).toBe(true);
	});

	it('filters by status', () => {
		expect(matchesFilter(thread(), { ...NO_FILTER, status: 'closed' }, null)).toBe(true);
		expect(matchesFilter(thread(), { ...NO_FILTER, status: 'failed' }, null)).toBe(false);
	});

	it('filters to the conversations the person started, and none when they are unknown', () => {
		expect(matchesFilter(thread(), { ...NO_FILTER, mine: true }, mine)).toBe(true);
		expect(matchesFilter(thread(), { ...NO_FILTER, mine: true }, new Set(['other']))).toBe(false);
		expect(matchesFilter(thread(), { ...NO_FILTER, mine: true }, null)).toBe(false);
	});

	it('filters by a custom range, both minutes included', () => {
		const f = (from: string | null, to: string | null) => ({ ...NO_FILTER, range: 'custom' as const, from, to });
		expect(matchesFilter(thread(), f('2026-11-10T12:00', '2026-11-10T12:00'), null)).toBe(true);
		expect(matchesFilter(thread(), f('2026-11-10T12:01', null), null)).toBe(false);
		expect(matchesFilter(thread(), f(null, '2026-11-10T11:59'), null)).toBe(false);
		expect(matchesFilter(thread({ startedAt: 'garbage' }), f('2026-11-01T00:00', null), null)).toBe(false);
	});

	it('filters by a preset range from the given time', () => {
		const now = new Date('2026-11-10T12:30:00');
		expect(matchesFilter(thread(), { ...NO_FILTER, range: '1h' }, null, now)).toBe(true);
		expect(matchesFilter(thread(), { ...NO_FILTER, range: '10m' }, null, now)).toBe(false);
		expect(matchesFilter(thread(), { ...NO_FILTER, range: '24h' }, null, new Date('2026-11-12T12:00:00'))).toBe(false);
	});

	it('searches the question and the message text, ignoring case', () => {
		const t = thread({}, [{ messageType: 'synthesis', content: 'The node runs at 83C under load' } as never]);
		expect(matchesFilter(t, { ...NO_FILTER, q: 'gpu NODE' }, null)).toBe(true);
		expect(matchesFilter(t, { ...NO_FILTER, q: 'gpu fan' }, null)).toBe(false);
		expect(matchesFilter(t, { ...NO_FILTER, q: '83c' }, null)).toBe(true);
		expect(matchesFilter(t, { ...NO_FILTER, q: 'zebra' }, null)).toBe(false);
	});

	it('drops what holds an excluded word and keeps the rest', () => {
		const t = thread({ userQuery: 'Smoke check smoke-17: reply with ok' });
		expect(matchesFilter(t, { ...NO_FILTER, q: 'reply -smoke' }, null)).toBe(false);
		expect(matchesFilter(thread(), { ...NO_FILTER, q: '-smoke' }, null)).toBe(true);
		expect(matchesFilter(t, { ...NO_FILTER, q: '-smoke' }, null)).toBe(false);
	});

	it('requires every filter at once', () => {
		expect(matchesFilter(thread(), { ...NO_FILTER, status: 'closed', q: 'zebra' }, null)).toBe(false);
	});
});

describe('highlighting', () => {
	it('marks every match and keeps the text', () => {
		const pieces = highlight('Gpu and gpu', 'GPU');
		expect(pieces.map((p) => p.text).join('')).toBe('Gpu and gpu');
		expect(pieces.filter((p) => p.hit).map((p) => p.text)).toEqual(['Gpu', 'gpu']);
	});

	it('marks each word and phrase, and not an excluded one', () => {
		const pieces = highlight('The GPU node: smoke check', 'gpu "smoke check" -node');
		expect(pieces.filter((p) => p.hit).map((p) => p.text)).toEqual(['GPU', 'smoke check']);
	});

	it('marks nothing for an empty query', () => {
		expect(highlight('abc', ' ')).toEqual([{ text: 'abc', hit: false }]);
	});

	it('cuts a long text to a passage around the match', () => {
		const text = `${'a'.repeat(200)} needle ${'b'.repeat(200)}`;
		const s = snippet(text, 'needle');
		expect(s).toContain('needle');
		expect(s.length).toBeLessThan(160);
		expect(s.startsWith('...') && s.endsWith('...')).toBe(true);
		expect(snippet('short', 'sh')).toBe('short');
	});

	it('finds the first text that holds the query', () => {
		expect(firstHit(thread(), 'gpu')).toBe('Why is the GPU node hot?');
		expect(firstHit(thread(), '')).toBeUndefined();
	});
});
