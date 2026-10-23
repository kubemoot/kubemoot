import { describe, expect, it } from 'vitest';
import { errorOnFailure, loadingOnStart } from './refresh-state.js';

describe('loadingOnStart', () => {
	it('shows the indicator on the first load', () => {
		expect(loadingOnStart(false)).toBe(true);
	});
	it('keeps the content on screen during a refresh', () => {
		expect(loadingOnStart(true)).toBe(false);
	});
});

describe('errorOnFailure', () => {
	it('reports the message when the first load fails', () => {
		expect(errorOnFailure(false, new Error('boom'), 'fallback')).toBe('boom');
	});
	it('uses the fallback for a non-Error failure', () => {
		expect(errorOnFailure(false, 'x', 'fallback')).toBe('fallback');
	});
	it('keeps the last data when a refresh fails', () => {
		expect(errorOnFailure(true, new Error('boom'), 'fallback')).toBeNull();
	});
});
