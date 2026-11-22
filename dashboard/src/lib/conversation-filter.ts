// The filters and the search of the conversation list. They live in the URL query, so a link
// reproduces the view: ?status=failed&mine=1&from=2026-11-01&to=2026-11-21&q=gpu. Everything
// here is a plain function of the loaded threads; the crew is the top-bar selector's.

import type { Thread } from '#lib/stores/discussions.js';
import { threadConversationId } from '#lib/conversation-export.js';
import { matchesSearch, parseSearch } from '#lib/search-query.js';

export type StatusFilter = 'active' | 'closed' | 'failed';
export const STATUSES: readonly StatusFilter[] = ['active', 'closed', 'failed'];

export interface ListFilter {
	status: StatusFilter | null;
	/** Only conversations the signed-in person started. */
	mine: boolean;
	/** Local dates, YYYY-MM-DD, both ends included. */
	from: string | null;
	to: string | null;
	/** Text to find in the question and the messages. */
	q: string;
}

export const NO_FILTER: ListFilter = { status: null, mine: false, from: null, to: null, q: '' };

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const MAX_QUERY = 200;

function validDate(value: string | null): string | null {
	return value !== null && DATE.test(value) && !Number.isNaN(Date.parse(`${value}T00:00:00`)) ? value : null;
}

/** The filter a query string describes; anything unreadable is ignored. */
export function parseFilter(params: URLSearchParams): ListFilter {
	const status = params.get('status');
	return {
		status: STATUSES.find((s) => s === status) ?? null,
		mine: params.get('mine') === '1',
		from: validDate(params.get('from')),
		to: validDate(params.get('to')),
		q: (params.get('q') ?? '').trim().slice(0, MAX_QUERY)
	};
}

/** Writes a filter into a copy of the query string, leaving other parameters (such as thread) as they are. */
export function withFilter(params: URLSearchParams, filter: ListFilter): URLSearchParams {
	const next = new URLSearchParams(params);
	for (const key of ['status', 'mine', 'from', 'to', 'q']) next.delete(key);
	if (filter.status) next.set('status', filter.status);
	if (filter.mine) next.set('mine', '1');
	if (filter.from) next.set('from', filter.from);
	if (filter.to) next.set('to', filter.to);
	if (filter.q) next.set('q', filter.q);
	return next;
}

export interface Chip {
	key: keyof ListFilter;
	label: string;
}

/** One removable chip for each filter that is set. */
export function chipsOf(filter: ListFilter): Chip[] {
	const chips: Chip[] = [];
	if (filter.status) chips.push({ key: 'status', label: `Status: ${filter.status}` });
	if (filter.mine) chips.push({ key: 'mine', label: 'Mine' });
	if (filter.from) chips.push({ key: 'from', label: `From ${filter.from}` });
	if (filter.to) chips.push({ key: 'to', label: `To ${filter.to}` });
	if (filter.q) chips.push({ key: 'q', label: `Search: ${filter.q}` });
	return chips;
}

/** The filter with one chip's setting cleared. */
export function withoutChip(filter: ListFilter, key: keyof ListFilter): ListFilter {
	return { ...filter, [key]: NO_FILTER[key] };
}

/** active while the crew is working, failed when an agent reported a failure or the thread errored, else closed. */
export function threadState(thread: Pick<Thread, 'status' | 'messages'>): StatusFilter {
	const failed = thread.messages.some(
		(m) => (m.messageType as string) === 'error' || Boolean(m.metadata?.failureType)
	);
	if (failed) return 'failed';
	return thread.status === 'open' ? 'active' : 'closed';
}

function localDate(iso: string): string | null {
	const t = new Date(iso);
	if (Number.isNaN(t.getTime())) return null;
	const pad = (n: number) => String(n).padStart(2, '0');
	return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}`;
}

function inRange(startedAt: string, filter: ListFilter): boolean {
	if (!filter.from && !filter.to) return true;
	const day = localDate(startedAt);
	if (day === null) return false;
	return (!filter.from || day >= filter.from) && (!filter.to || day <= filter.to);
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
export function matchesFilter(thread: Thread, filter: ListFilter, mineIds: ReadonlySet<string> | null): boolean {
	if (filter.status && threadState(thread) !== filter.status) return false;
	if (filter.mine && !(mineIds && mineIds.has(threadConversationId(thread) ?? ''))) return false;
	if (!inRange(thread.startedAt, filter)) return false;
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
