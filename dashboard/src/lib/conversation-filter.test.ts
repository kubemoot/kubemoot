import { describe, expect, it } from 'vitest';
import type { Thread } from '#lib/stores/discussions.js';
import {
	NO_FILTER,
	chipsOf,
	firstHit,
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
		const f = { status: 'failed', mine: true, from: '2026-11-01', to: '2026-11-21', q: 'gpu' } as const;
		const params = withFilter(new URLSearchParams('thread=abc'), f);
		expect(params.toString()).toBe('thread=abc&status=failed&mine=1&from=2026-11-01&to=2026-11-21&q=gpu');
		expect(parseFilter(params)).toEqual(f);
	});

	it('drops what it cannot read', () => {
		expect(parseFilter(new URLSearchParams('status=bogus&from=yesterday&to=2026-13-45&mine=yes&q=%20%20'))).toEqual(NO_FILTER);
	});

	it('removes a filter that is cleared', () => {
		expect(withFilter(new URLSearchParams('status=failed&q=x'), NO_FILTER).toString()).toBe('');
	});

	it('bounds the search text', () => {
		expect(parseFilter(new URLSearchParams({ q: 'x'.repeat(500) })).q).toHaveLength(200);
	});
});

describe('chips', () => {
	it('lists one removable chip per filter set', () => {
		const f = { status: 'active', mine: true, from: '2026-11-01', to: null, q: 'gpu' } as const;
		expect(chipsOf(f).map((c) => c.key)).toEqual(['status', 'mine', 'from', 'q']);
		expect(chipsOf(withoutChip(f, 'mine')).map((c) => c.key)).toEqual(['status', 'from', 'q']);
		expect(chipsOf(NO_FILTER)).toEqual([]);
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

	it('filters by date, both ends included', () => {
		const f = (from: string | null, to: string | null) => ({ ...NO_FILTER, from, to });
		expect(matchesFilter(thread(), f('2026-11-10', '2026-11-10'), null)).toBe(true);
		expect(matchesFilter(thread(), f('2026-11-11', null), null)).toBe(false);
		expect(matchesFilter(thread(), f(null, '2026-11-09'), null)).toBe(false);
		expect(matchesFilter(thread({ startedAt: 'garbage' }), f('2026-11-01', null), null)).toBe(false);
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
