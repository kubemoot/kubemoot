import { describe, expect, it } from 'vitest';
import { compareCodeUnits, describeError, splitCamelCase, trimTrailingSlashes } from './text-utils';

describe('trimTrailingSlashes', () => {
	it('removes one or many trailing slashes', () => {
		expect(trimTrailingSlashes('https://ollama:11434/')).toBe('https://ollama:11434');
		expect(trimTrailingSlashes('https://ollama:11434///')).toBe('https://ollama:11434');
	});

	it('keeps a value without a trailing slash and inner slashes', () => {
		expect(trimTrailingSlashes('https://a/b')).toBe('https://a/b');
	});

	it('handles the empty string and an all-slash string', () => {
		expect(trimTrailingSlashes('')).toBe('');
		expect(trimTrailingSlashes('////')).toBe('');
	});
});

describe('compareCodeUnits', () => {
	it('orders by UTF-16 code unit, not by locale', () => {
		const names = ['b', 'a_1', 'a1', 'B', 'a-1', 'a'];
		expect([...names].sort(compareCodeUnits)).toEqual(['B', 'a', 'a-1', 'a1', 'a_1', 'b']);
	});

	it('returns 0 for equal strings', () => {
		expect(compareCodeUnits('x', 'x')).toBe(0);
	});
});

describe('splitCamelCase', () => {
	it('puts a space at each lower-to-upper boundary', () => {
		expect(splitCamelCase('NotReady')).toBe('Not Ready');
		expect(splitCamelCase('CrashLoopBackOff')).toBe('Crash Loop Back Off');
	});

	it('leaves words without such a boundary alone', () => {
		expect(splitCamelCase('Ready')).toBe('Ready');
		expect(splitCamelCase('GPU')).toBe('GPU');
		expect(splitCamelCase('')).toBe('');
	});
});

describe('describeError', () => {
	it('passes a string through', () => {
		expect(describeError('gone')).toBe('gone');
	});

	it('formats an Error the way String() does', () => {
		expect(describeError(new TypeError('bad'))).toBe('TypeError: bad');
	});

	it('serializes a plain object instead of [object Object]', () => {
		expect(describeError({ code: 410 })).toBe('{"code":410}');
	});

	it('falls back to String() for values JSON cannot represent', () => {
		expect(describeError(undefined)).toBe('undefined');
	});
});
