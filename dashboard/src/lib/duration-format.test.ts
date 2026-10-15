import { describe, expect, it } from 'vitest';
import { formatDuration, formatElapsed } from './duration-format';

describe('formatDuration', () => {
	it.each([
		[0, '0ms'],
		[850, '850ms'],
		[3_449, '3.4s'],
		[9_999, '9.9s'],
		[12_000, '12.0s'],
		[12_340, '12.3s'],
		[59_999, '59.9s'],
		[72_000, '1m12s'],
		[61_000, '1m01s'],
		[3_600_000, '1h00m'],
		[3_725_000, '1h02m']
	])('formats %i ms as %s', (ms, text) => {
		expect(formatDuration(ms)).toBe(text);
	});

	it.each([undefined, Number.NaN, -1, Number.POSITIVE_INFINITY])('is empty for %s', (ms) => {
		expect(formatDuration(ms)).toBe('');
	});
});

describe('formatElapsed', () => {
	it('prefixes a plus sign', () => {
		expect(formatElapsed(72_000)).toBe('+1m12s');
		expect(formatElapsed(4_200)).toBe('+4.2s');
	});

	it('shows the thread start and anything within its first second as +0s', () => {
		expect(formatElapsed(0)).toBe('+0s');
		expect(formatElapsed(999)).toBe('+0s');
	});

	it('is empty when unknown or before the start', () => {
		expect(formatElapsed(undefined)).toBe('');
		expect(formatElapsed(Number.NaN)).toBe('');
		expect(formatElapsed(-500)).toBe('');
	});
});
