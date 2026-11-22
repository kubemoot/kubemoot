// The filters and the search of the conversation list. They live in the URL query, so a link
// reproduces the view: ?status=failed&mine=1&range=custom&from=2026-11-01T09:00&to=2026-11-21T17:30&q=gpu. Everything
// here is a plain function of the loaded threads; the crew is the top-bar selector's.

import { threadStartedAt, type Thread } from '#lib/stores/discussions.js';
import { threadConversationId } from '#lib/conversation-export.js';
import { matchesSearch, parseSearch } from '#lib/search-query.js';
import { DEFAULT_RANGE, customLabel, inTimeRange, isRangeKey, parseLocal, rangeLabel, type RangeKey } from '#lib/time-range.js';

export type StatusFilter = 'active' | 'closed' | 'failed';
export const STATUSES: readonly StatusFilter[] = ['active', 'closed', 'failed'];

export interface ListFilter {
	status: StatusFilter | null;
	/** Only conversations the signed-in person started. */
	mine: boolean;
	/** The time range; with custom, from and to are local date-times, YYYY-MM-DDTHH:mm, both minutes included. */
	range: RangeKey;
	from: string | null;
	to: string | null;
	/** Text to find in the question and the messages. */
	q: string;
}

/** Nothing filtered: every status, everyone's, any time. */
export const NO_FILTER: ListFilter = { status: null, mine: false, range: 'any', from: null, to: null, q: '' };

/** What a list shows until a person changes it: the default time range, nothing else. */
export const DEFAULT_FILTER: ListFilter = { ...NO_FILTER, range: DEFAULT_RANGE };

const MAX_QUERY = 200;

const FILTER_KEYS = ['status', 'mine', 'range', 'from', 'to', 'q'] as const;

/** Whether the query string sets any filter; without one, the viewer's remembered filter applies. */
export function hasFilterParams(params: URLSearchParams): boolean {
	return FILTER_KEYS.some((key) => params.has(key));
}

/** The filter a query string describes; anything unreadable is ignored, and no range means the default. */
export function parseFilter(params: URLSearchParams): ListFilter {
	const status = params.get('status');
	const asked = params.get('range');
	const from = parseLocal(params.get('from'));
	const to = parseLocal(params.get('to'), true);
	// An address from before ranges existed carries only from and to: that is a custom range.
	const legacy = from || to ? 'custom' : DEFAULT_RANGE;
	const range = isRangeKey(asked) ? asked : legacy;
	const custom = range === 'custom';
	return {
		status: STATUSES.find((s) => s === status) ?? null,
		mine: params.get('mine') === '1',
		range,
		from: custom ? from : null,
		to: custom ? to : null,
		q: (params.get('q') ?? '').trim().slice(0, MAX_QUERY)
	};
}

/** Writes a filter into a copy of the query string, leaving other parameters (such as thread) as they are. */
export function withFilter(params: URLSearchParams, filter: ListFilter): URLSearchParams {
	const next = new URLSearchParams(params);
	for (const key of FILTER_KEYS) next.delete(key);
	if (filter.status) next.set('status', filter.status);
	if (filter.mine) next.set('mine', '1');
	if (filter.range !== DEFAULT_RANGE) next.set('range', filter.range);
	if (filter.range === 'custom' && filter.from) next.set('from', filter.from);
	if (filter.range === 'custom' && filter.to) next.set('to', filter.to);
	if (filter.q) next.set('q', filter.q);
	return next;
}

export interface Chip {
	key: keyof ListFilter;
	label: string;
}

/** The chip for the time range, when one is set. */
function rangeChip(filter: ListFilter): Chip[] {
	if (filter.range === 'any') return [];
	if (filter.range === 'custom') return filter.from || filter.to ? [{ key: 'range', label: customLabel(filter.from, filter.to) }] : [];
	return [{ key: 'range', label: rangeLabel(filter.range) }];
}

/** One removable chip for each filter that is set. */
export function chipsOf(filter: ListFilter): Chip[] {
	const chips: Chip[] = [];
	if (filter.status) chips.push({ key: 'status', label: `Status: ${filter.status}` });
	if (filter.mine) chips.push({ key: 'mine', label: 'Mine' });
	chips.push(...rangeChip(filter));
	if (filter.q) chips.push({ key: 'q', label: `Search: ${filter.q}` });
	return chips;
}

/** The filter with one chip's setting cleared; clearing the time range means any time. */
export function withoutChip(filter: ListFilter, key: keyof ListFilter): ListFilter {
	if (key === 'range') return { ...filter, range: 'any', from: null, to: null };
	return { ...filter, [key]: NO_FILTER[key] };
}

/**
 * Why a list that shows nothing is empty, when a filter is the reason, else null. `noun` is what
 * the list holds, in the plural: "conversations", "discussions".
 */
export function emptyBecauseOfFilter(filter: ListFilter, noun: string): string | null {
	if (filter.q) return `No ${noun} match this search.`;
	if (filter.status || filter.mine) return `No ${noun} match these filters.`;
	if (filter.range !== 'any') return `No ${noun} in this time range. Choose Anytime to see older ones.`;
	return null;
}

/** active while the crew is working, failed when an agent reported a failure or the thread errored, else closed. */
export function threadState(thread: Pick<Thread, 'status' | 'messages'>): StatusFilter {
	const failed = thread.messages.some(
		(m) => (m.messageType as string) === 'error' || Boolean(m.metadata?.failureType)
	);
	if (failed) return 'failed';
	return thread.status === 'open' ? 'active' : 'closed';
}

/** The text a search looks in: the question, then every message's content. */
export function searchableText(thread: Pick<Thread, 'userQuery' | 'messages'>): string[] {
	return [thread.userQuery, ...thread.messages.map((m) => m.content ?? '')].filter((t) => t !== '');
}

/** The first text of the thread that holds one of the query's words or phrases, or undefined. */
export function firstHit(thread: Pick<Thread, 'userQuery' | 'messages'>, query: string): string | undefined {
	const { include } = parseSearch(query);
	if (include.length === 0) return undefined;
	return searchableText(thread).find((t) => include.some((term) => t.toLowerCase().includes(term)));
}

/**
 * Whether a thread passes the filter. `mineIds` are the conversation ids the signed-in person
 * started (from the crew's gateway); with mine set and none known the thread is left out.
 */
export function matchesFilter(
	thread: Thread,
	filter: ListFilter,
	mineIds: ReadonlySet<string> | null,
	now: Date = new Date()
): boolean {
	if (filter.status && threadState(thread) !== filter.status) return false;
	if (filter.mine && !(mineIds && mineIds.has(threadConversationId(thread) ?? ''))) return false;
	if (!inTimeRange(threadStartedAt(thread), { key: filter.range, from: filter.from, to: filter.to }, now)) return false;
	return matchesSearch(searchableText(thread), parseSearch(filter.q));
}

export interface Piece {
	text: string;
	hit: boolean;
}

/** The ranges of the text that hold a word or phrase of the query, in order and not overlapping. */
function hitRanges(lower: string, terms: readonly string[]): [number, number][] {
	const ranges: [number, number][] = [];
	for (const term of terms) {
		for (let i = lower.indexOf(term); i !== -1; i = lower.indexOf(term, i + term.length)) ranges.push([i, i + term.length]);
	}
	ranges.sort((a, b) => a[0] - b[0] || b[1] - a[1]);
	const merged: [number, number][] = [];
	for (const range of ranges) {
		const last = merged.at(-1);
		if (last && range[0] < last[1]) last[1] = Math.max(last[1], range[1]);
		else merged.push([...range]);
	}
	return merged;
}

/** A text cut into pieces, those that match a word or phrase of the query marked; for highlighting with no HTML. */
export function highlight(text: string, query: string): Piece[] {
	const ranges = hitRanges(text.toLowerCase(), parseSearch(query).include);
	if (ranges.length === 0) return [{ text, hit: false }];
	const pieces: Piece[] = [];
	let at = 0;
	for (const [start, end] of ranges) {
		if (start > at) pieces.push({ text: text.slice(at, start), hit: false });
		pieces.push({ text: text.slice(start, end), hit: true });
		at = end;
	}
	if (at < text.length) pieces.push({ text: text.slice(at), hit: false });
	return pieces;
}

/** A short passage around the first match, so a long message shows what matched. */
export function snippet(text: string, query: string, radius = 60): string {
	const first = hitRanges(text.toLowerCase(), parseSearch(query).include)[0];
	if (!first || text.length <= radius * 2) return text;
	const start = Math.max(0, first[0] - radius);
	const end = Math.min(text.length, first[1] + radius);
	return `${start > 0 ? '...' : ''}${text.slice(start, end)}${end < text.length ? '...' : ''}`;
}
