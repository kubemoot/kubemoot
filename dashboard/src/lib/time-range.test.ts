import { describe, expect, it } from 'vitest';
import { RANGES, customLabel, inTimeRange, isRangeKey, parseLocal, rangeLabel, rangeStart } from './time-range.js';

// Wednesday 2026-11-18, 14:30 local.
const NOW = new Date(2026, 10, 18, 14, 30);
const at = (y: number, mo: number, d: number, h = 0, mi = 0) => new Date(y, mo - 1, d, h, mi).toISOString();
const inRange = (key: Parameters<typeof inTimeRange>[1]['key'], when: string) => inTimeRange(when, { key, from: null, to: null }, NOW);

describe('the list of ranges', () => {
	it('offers Anytime first and Custom last, each with a label', () => {
		expect(RANGES[0]).toBe('any');
		expect(RANGES.at(-1)).toBe('custom');
		expect(RANGES.map(rangeLabel)).toEqual([
			'Anytime', 'Last 10 minutes', 'Last hour', 'Last 6 hours', 'Today', 'Last 24 hours', 'Last 3 days', 'This week', 'This month', 'Last 3 months', 'Custom...'
		]);
	});

	it('knows a range key from other text', () => {
		expect(isRangeKey('24h')).toBe(true);
		expect(isRangeKey('forever')).toBe(false);
		expect(isRangeKey(null)).toBe(false);
	});
});

describe('relative ranges', () => {
	it('include the instants inside the window and not the ones before it', () => {
		expect(inRange('10m', at(2026, 11, 18, 14, 21))).toBe(true);
		expect(inRange('10m', at(2026, 11, 18, 14, 19))).toBe(false);
		expect(inRange('1h', at(2026, 11, 18, 13, 31))).toBe(true);
		expect(inRange('1h', at(2026, 11, 18, 13, 29))).toBe(false);
		expect(inRange('6h', at(2026, 11, 18, 8, 31))).toBe(true);
		expect(inRange('6h', at(2026, 11, 18, 8, 29))).toBe(false);
		expect(inRange('24h', at(2026, 11, 17, 14, 31))).toBe(true);
		expect(inRange('24h', at(2026, 11, 17, 14, 29))).toBe(false);
		expect(inRange('3d', at(2026, 11, 15, 14, 31))).toBe(true);
		expect(inRange('3d', at(2026, 11, 15, 14, 29))).toBe(false);
	});
});

describe('calendar ranges', () => {
	it('Today starts at local midnight', () => {
		expect(inRange('today', at(2026, 11, 18, 0, 0))).toBe(true);
		expect(inRange('today', at(2026, 11, 17, 23, 59))).toBe(false);
	});

	it('This week starts on Monday', () => {
		expect(inRange('week', at(2026, 11, 16, 0, 0))).toBe(true);
		expect(inRange('week', at(2026, 11, 15, 23, 59))).toBe(false);
		// On a Sunday the week still started the Monday before.
		const sunday = new Date(2026, 10, 22, 9, 0);
		expect(rangeStart('week', sunday)).toBe(new Date(2026, 10, 16).getTime());
	});

	it('This month starts on the first', () => {
		expect(inRange('month', at(2026, 11, 1, 0, 0))).toBe(true);
		expect(inRange('month', at(2026, 10, 31, 23, 59))).toBe(false);
	});

	it('Last 3 months reaches back three calendar months', () => {
		expect(inRange('3mo', at(2026, 8, 18, 14, 31))).toBe(true);
		expect(inRange('3mo', at(2026, 8, 18, 14, 29))).toBe(false);
	});
});

describe('Last 3 months at the end of a month', () => {
	it('keeps the day inside the shorter month instead of spilling into the next', () => {
		const thirtyFirstMay = new Date(2026, 4, 31, 12, 0);
		expect(rangeStart('3mo', thirtyFirstMay)).toBe(new Date(2026, 1, 28, 12, 0).getTime());
	});
});

describe('Anytime and custom ranges', () => {
	it('Anytime includes everything but a time that cannot be read is left out of the others', () => {
		expect(inRange('any', at(2001, 1, 1))).toBe(true);
		expect(inRange('1h', 'garbage')).toBe(false);
	});

	it('a custom range includes both of its minutes and either end may be open', () => {
		const range = { key: 'custom' as const, from: '2026-11-18T09:00', to: '2026-11-18T10:00' };
		expect(inTimeRange(at(2026, 11, 18, 9, 0), range, NOW)).toBe(true);
		expect(inTimeRange(at(2026, 11, 18, 10, 0), range, NOW)).toBe(true);
		expect(inTimeRange(at(2026, 11, 18, 8, 59), range, NOW)).toBe(false);
		expect(inTimeRange(at(2026, 11, 18, 10, 1), range, NOW)).toBe(false);
		expect(inTimeRange(at(2020, 1, 1), { ...range, from: null }, NOW)).toBe(true);
		expect(inTimeRange(at(2030, 1, 1), { ...range, to: null }, NOW)).toBe(true);
	});
});

describe('reading a local date and time', () => {
	it('accepts a date-time, and reads a bare date as the start or end of that day', () => {
		expect(parseLocal('2026-11-18T09:05')).toBe('2026-11-18T09:05');
		expect(parseLocal('2026-11-18')).toBe('2026-11-18T00:00');
		expect(parseLocal('2026-11-18', true)).toBe('2026-11-18T23:59');
	});

	it('rejects what is not a real local date and time', () => {
		for (const bad of [null, '', 'yesterday', '2026-13-45', '2026-11-18T25:00', '2026-11-18T09:05:30']) expect(parseLocal(bad)).toBeNull();
	});
});

describe('custom label', () => {
	it('names both ends, one end, or nothing set', () => {
		expect(customLabel('2026-11-01T09:00', '2026-11-21T17:30')).toMatch(/^Nov 1, 09:00 to Nov 21, 17:30$/);
		expect(customLabel('2026-11-01T09:00', null)).toMatch(/^From Nov 1/);
		expect(customLabel(null, '2026-11-21T17:30')).toMatch(/^Until Nov 21/);
		expect(customLabel(null, null)).toBe('Custom...');
	});
});
