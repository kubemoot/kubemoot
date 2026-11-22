import { describe, expect, it } from 'vitest';
import { DEFAULT_FILTER, NO_FILTER } from './conversation-filter.js';
import { listAddress, mineScopesFor } from './list-scope.js';

describe('listAddress', () => {
	it('writes the filter into the query and keeps the open conversation', () => {
		expect(listAddress('/conversations', '?conversation=c-1&q=old', { ...DEFAULT_FILTER, q: 'namespace -smoke', mine: true })).toBe(
			'/conversations?conversation=c-1&mine=1&q=namespace+-smoke'
		);
	});

	it('has no query when the filter is empty and nothing is open', () => {
		expect(listAddress('/moot', '?q=gpu', DEFAULT_FILTER)).toBe('/moot');
	});

	it('keeps Anytime in the address, since the default is the last 24 hours', () => {
		expect(listAddress('/moot', '', NO_FILTER)).toBe('/moot?range=any');
	});
});

describe('mineScopesFor', () => {
	const directory = [
		{ namespace: 'crew-a', crew: 'a', displayName: 'A' },
		{ namespace: 'crew-b', crew: 'b', displayName: 'B' }
	];

	it('is the selected crew alone, or every crew when none is selected', () => {
		expect(mineScopesFor({ namespace: 'crew-a', crew: 'a' }, directory)).toEqual([{ namespace: 'crew-a', crew: 'a' }]);
		expect(mineScopesFor(null, directory)).toHaveLength(2);
		expect(mineScopesFor(null, [])).toEqual([]);
	});

	it('leaves out a crew with no usable name', () => {
		expect(mineScopesFor(null, [{ namespace: 'Bad Name', crew: 'x', displayName: '' }, ...directory])).toHaveLength(2);
	});
});
