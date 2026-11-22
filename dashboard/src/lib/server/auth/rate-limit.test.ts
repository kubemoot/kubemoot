import { beforeEach, describe, expect, it } from 'vitest';
import { IN_TOTAL, PER_CLIENT, WINDOW_MS, allowSignInAttempt, resetSignInAttempts } from './rate-limit';

beforeEach(() => resetSignInAttempts());

describe('allowSignInAttempt', () => {
	it('allows a number of attempts per client in a window, then refuses, then allows again', () => {
		for (let i = 0; i < PER_CLIENT; i++) expect(allowSignInAttempt('a', 1000)).toBe(true);
		expect(allowSignInAttempt('a', 1000)).toBe(false);
		expect(allowSignInAttempt('b', 1000)).toBe(true);
		expect(allowSignInAttempt('a', 1000 + WINDOW_MS + 1)).toBe(true);
	});

	it('limits the whole dashboard too', () => {
		for (let i = 0; i < IN_TOTAL; i++) expect(allowSignInAttempt(`c${i}`, 1000)).toBe(true);
		expect(allowSignInAttempt('fresh', 1000)).toBe(false);
	});
});
