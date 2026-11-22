import { afterEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_FILTER, NO_FILTER } from './conversation-filter.js';
import { loadFilter, saveFilter } from './filter-storage.js';

afterEach(() => {
	vi.unstubAllGlobals();
	localStorage.clear();
});

describe('the remembered filter', () => {
	it('is none until one is saved', () => {
		expect(loadFilter('conversations')).toBeNull();
	});

	it('comes back as saved, per page', () => {
		const filter = { status: 'failed', mine: true, range: 'custom', from: '2026-11-01T09:00', to: null, q: 'gpu' } as const;
		saveFilter('conversations', filter);
		expect(loadFilter('conversations')).toEqual(filter);
		expect(loadFilter('moot')).toBeNull();
		saveFilter('moot', NO_FILTER);
		expect(loadFilter('moot')).toEqual(NO_FILTER);
		expect(loadFilter('conversations')).toEqual(filter);
	});

	it('keeps only what a filter can hold, so a stale or edited entry cannot break the list', () => {
		localStorage.setItem(
			'kubemoot-dashboard-filter-moot',
			JSON.stringify({ status: 'bogus', mine: 'yes', range: 'forever', from: '2026-11-01T09:00', q: 42, extra: true })
		);
		expect(loadFilter('moot')).toEqual({ ...DEFAULT_FILTER, from: null, to: null });
	});

	it('is none for text that is not JSON or not an object', () => {
		localStorage.setItem('kubemoot-dashboard-filter-moot', '{oops');
		expect(loadFilter('moot')).toBeNull();
		localStorage.setItem('kubemoot-dashboard-filter-moot', '"text"');
		expect(loadFilter('moot')).toBeNull();
	});
});

describe('with storage unavailable', () => {
	function blocked() {
		const refuse = () => {
			throw new Error('blocked');
		};
		vi.stubGlobal('localStorage', { getItem: refuse, setItem: refuse });
	}

	it('remembers nothing and does not throw, so the lists open on the default', () => {
		blocked();
		expect(() => saveFilter('conversations', NO_FILTER)).not.toThrow();
		expect(loadFilter('conversations')).toBeNull();
	});

	it('does not throw when the storage object is missing', () => {
		vi.stubGlobal('localStorage', undefined);
		expect(() => saveFilter('moot', NO_FILTER)).not.toThrow();
		expect(loadFilter('moot')).toBeNull();
	});
});
