import { describe, expect, it } from 'vitest';
import { formatBytes, formatMiB } from './format';

describe('formatMiB', () => {
	it('shows MiB below 1024 and GiB from 1024', () => {
		expect(formatMiB(800)).toBe('800 MiB');
		expect(formatMiB(9507)).toBe('9.3 GiB');
		expect(formatMiB(1024)).toBe('1.0 GiB');
	});

	it('rounds before choosing the unit', () => {
		expect(formatMiB(1023.6)).toBe('1.0 GiB');
		expect(formatMiB(1023.4)).toBe('1023 MiB');
	});

	it('shows a dash for a missing, zero, negative, or non-finite value', () => {
		for (const bad of [undefined, null, 0, -1, NaN, Infinity]) {
			expect(formatMiB(bad)).toBe('-');
			expect(formatBytes(bad)).toBe('-');
		}
	});
});

describe('formatBytes', () => {
	it('converts bytes to MiB first', () => {
		expect(formatBytes(800 * 1024 * 1024)).toBe('800 MiB');
		expect(formatBytes(9507 * 1024 * 1024)).toBe('9.3 GiB');
	});
});
