// The filter a viewer last chose on the Conversations and Moot lists, kept in this browser so
// the list opens the way they left it. Storage can be missing or refuse (a private window,
// blocked site data): every call then does nothing and the lists open on the default filter.

import { DEFAULT_FILTER, STATUSES, type ListFilter } from '#lib/conversation-filter.js';
import { readItem, writeItem } from '#lib/safe-storage.js';
import { isRangeKey, parseLocal } from '#lib/time-range.js';

export type FilterPage = 'conversations' | 'moot';

const key = (page: FilterPage) => `kubemoot-dashboard-filter-${page}`;

/** Keeps only what a filter can hold, so a stale or edited entry cannot break the list. */
function sanitize(raw: unknown): ListFilter | null {
	if (!raw || typeof raw !== 'object') return null;
	const r = raw as Record<string, unknown>;
	const text = (v: unknown) => (typeof v === 'string' ? v : null);
	const range = isRangeKey(text(r.range)) ? (r.range as ListFilter['range']) : DEFAULT_FILTER.range;
	const custom = range === 'custom';
	return {
		status: STATUSES.find((s) => s === r.status) ?? null,
		mine: r.mine === true,
		range,
		from: custom ? parseLocal(text(r.from)) : null,
		to: custom ? parseLocal(text(r.to), true) : null,
		q: (text(r.q) ?? '').slice(0, 200)
	};
}

/** The remembered filter, or null when none is kept or storage is unavailable. */
export function loadFilter(page: FilterPage): ListFilter | null {
	try {
		const text = readItem(key(page));
		return text ? sanitize(JSON.parse(text)) : null;
	} catch {
		return null;
	}
}

/** Remembers the filter; does nothing when storage is unavailable or full. */
export function saveFilter(page: FilterPage, filter: ListFilter): void {
	writeItem(key(page), JSON.stringify(filter));
}
