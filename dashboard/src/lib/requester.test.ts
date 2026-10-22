import { describe, expect, it } from 'vitest';
import { currentRequester, isTimeZone, localDateTime } from './requester';

describe('isTimeZone', () => {
	it('accepts IANA zone names', () => {
		expect(isTimeZone('America/Los_Angeles')).toBe(true);
		expect(isTimeZone('UTC')).toBe(true);
	});

	it('rejects names that are not zones, and non-strings', () => {
		expect(isTimeZone('Mars/Olympus_Mons')).toBe(false);
		expect(isTimeZone('')).toBe(false);
		expect(isTimeZone('   ')).toBe(false);
		expect(isTimeZone(undefined)).toBe(false);
		expect(isTimeZone(42)).toBe(false);
	});
});

describe('localDateTime', () => {
	it('writes the local wall clock without an offset', () => {
		expect(localDateTime(new Date(2026, 9, 24, 9, 5, 3))).toBe('2026-10-24T09:05:03');
	});

	it('pads single digits', () => {
		expect(localDateTime(new Date(2026, 0, 2, 3, 4, 5))).toBe('2026-01-02T03:04:05');
	});
});

describe('currentRequester', () => {
	it('carries the given zone and the local date-time', () => {
		const now = new Date(2026, 9, 24, 9, 15, 0);
		expect(currentRequester(now, 'America/Los_Angeles')).toEqual({
			timeZone: 'America/Los_Angeles',
			localDateTime: '2026-10-24T09:15:00'
		});
	});

	it('uses the browser zone by default', () => {
		const requester = currentRequester();
		expect(requester?.timeZone).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
	});

	it('sends nothing when the zone is missing or invalid', () => {
		expect(currentRequester(new Date(), null)).toBeUndefined();
		expect(currentRequester(new Date(), 'Not/AZone')).toBeUndefined();
	});

	it('sends nothing for an invalid date', () => {
		expect(currentRequester(new Date('nope'), 'Asia/Tokyo')).toBeUndefined();
	});
});
