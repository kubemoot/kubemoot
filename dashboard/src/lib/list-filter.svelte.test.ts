import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { DEFAULT_FILTER, NO_FILTER } from '#lib/conversation-filter.js';
import { loadFilter } from '#lib/filter-storage.js';
import { setPageUrl } from '../../vitest-mocks/app-state.svelte';
import { createListFilter } from './list-filter.svelte.js';

beforeEach(() => {
	localStorage.clear();
	setPageUrl('/dashboard/moot');
});
afterEach(() => localStorage.clear());

describe('createListFilter', () => {
	it('is the default until a filter is chosen or restored', () => {
		expect(createListFilter('moot', '/moot').filter).toEqual(DEFAULT_FILTER);
	});

	it('restores what this viewer last chose when the address sets no filter', () => {
		localStorage.setItem('kubemoot-dashboard-filter-moot', JSON.stringify({ ...NO_FILTER, status: 'failed', range: '1h' }));
		const list = createListFilter('moot', '/moot');
		list.restore();
		expect(list.filter).toMatchObject({ status: 'failed', range: '1h' });
	});

	it('lets an address that sets a filter win over the remembered one', () => {
		localStorage.setItem('kubemoot-dashboard-filter-moot', JSON.stringify({ ...NO_FILTER, status: 'failed' }));
		const list = createListFilter('moot', '/moot');
		list.restore();
		setPageUrl('/dashboard/moot?range=any&q=gpu');
		expect(list.filter).toEqual({ ...NO_FILTER, q: 'gpu' });
	});

	it('remembers a change, per page, and applies it at once', () => {
		const list = createListFilter('moot', '/moot');
		list.set({ ...NO_FILTER, mine: true });
		expect(loadFilter('moot')).toEqual({ ...NO_FILTER, mine: true });
		expect(loadFilter('conversations')).toBeNull();
		expect(list.filter.mine).toBe(true);
	});
});
