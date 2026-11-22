// The filter of the Conversations and Moot lists as one page uses it: the address decides when
// it sets a filter, else the one this viewer last chose (kept in the browser), else the default.
// Both pages use this, so they cannot drift apart.

import { goto } from '$app/navigation';
import { page } from '$app/state';
import { DEFAULT_FILTER, hasFilterParams, parseFilter, type ListFilter } from '#lib/conversation-filter.js';
import { loadFilter, saveFilter, type FilterPage } from '#lib/filter-storage.js';
import { listAddress } from '#lib/list-scope.js';

export function createListFilter(name: FilterPage, route: '/conversations' | '/moot') {
	let restored = $state<ListFilter | null>(null);

	return {
		/** Reads the remembered filter. Called once in the browser after the page mounts, so the server and the first render agree. */
		restore(): void {
			restored = loadFilter(name);
		},
		get filter(): ListFilter {
			const params = new URLSearchParams(page.url.search);
			return hasFilterParams(params) ? parseFilter(params) : (restored ?? DEFAULT_FILTER);
		},
		/** Applies a change: remembers it and writes it into the address. */
		set(next: ListFilter): void {
			restored = next;
			saveFilter(name, next);
			void goto(listAddress(route, page.url.search, next), { replace: true, reset: false });
		}
	};
}
