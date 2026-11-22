import { describe, expect, it } from 'vitest';
import { isEmptySearch, matchesSearch, parseSearch } from './search-query.js';

describe('parseSearch', () => {
	it('reads words as terms that must match', () => {
		expect(parseSearch('namespace gpu')).toEqual({ include: ['namespace', 'gpu'], exclude: [] });
	});

	it('reads a leading dash as an exclusion', () => {
		expect(parseSearch('namespace -smoke')).toEqual({ include: ['namespace'], exclude: ['smoke'] });
	});

	it('reads a quoted phrase as one term', () => {
		expect(parseSearch('"gpu temperature" fan')).toEqual({ include: ['gpu temperature', 'fan'], exclude: [] });
	});

	it('reads an excluded phrase', () => {
		expect(parseSearch('-"smoke check" namespace')).toEqual({ include: ['namespace'], exclude: ['smoke check'] });
	});

	it('drops a stray dash and extra spaces', () => {
		expect(parseSearch('  -   namespace  - ')).toEqual({ include: ['namespace'], exclude: [] });
	});

	it('keeps a dash inside a word', () => {
		expect(parseSearch('crew-demo')).toEqual({ include: ['crew-demo'], exclude: [] });
	});

	it('reads empty and blank input as no terms', () => {
		expect(isEmptySearch(parseSearch(''))).toBe(true);
		expect(isEmptySearch(parseSearch('   '))).toBe(true);
		expect(parseSearch('""')).toEqual({ include: [], exclude: [] });
	});

	it('lower-cases terms', () => {
		expect(parseSearch('NameSpace -SMOKE "Gpu Temp"')).toEqual({ include: ['namespace', 'gpu temp'], exclude: ['smoke'] });
	});

	it('runs an unclosed quote to the end', () => {
		expect(parseSearch('"gpu temp')).toEqual({ include: ['gpu temp'], exclude: [] });
	});
});

describe('matchesSearch', () => {
	const texts = ['Which namespace is the pod in?', 'The pod runs in the default Namespace.'];

	it('needs every word, anywhere in the thread, in any case', () => {
		expect(matchesSearch(texts, parseSearch('NAMESPACE pod'))).toBe(true);
		expect(matchesSearch(texts, parseSearch('namespace gpu'))).toBe(false);
	});

	it('drops a thread that holds an excluded word', () => {
		expect(matchesSearch(texts, parseSearch('namespace -default'))).toBe(false);
		expect(matchesSearch(texts, parseSearch('namespace -smoke'))).toBe(true);
	});

	it('matches a phrase as written and not across messages', () => {
		expect(matchesSearch(texts, parseSearch('"the default namespace"'))).toBe(true);
		expect(matchesSearch(texts, parseSearch('"in? the"'))).toBe(false);
		expect(matchesSearch(texts, parseSearch('-"default namespace"'))).toBe(false);
	});

	it('works with only exclusions', () => {
		expect(matchesSearch(['Smoke check smoke-1: reply'], parseSearch('-smoke'))).toBe(false);
		expect(matchesSearch(texts, parseSearch('-smoke'))).toBe(true);
	});

	it('matches everything for an empty query', () => {
		expect(matchesSearch([], parseSearch(''))).toBe(true);
		expect(matchesSearch(texts, parseSearch('-'))).toBe(true);
	});
});
