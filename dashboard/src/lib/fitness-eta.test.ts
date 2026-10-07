import { describe, expect, it } from 'vitest';
import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';
import { etaText, fmtRemaining, meanDurationMs, suiteEta } from './fitness-eta';

const T0 = Date.parse('2026-10-06T10:00:00Z');
const HOUR = 3_600_000;

function status(over: Partial<NonNullable<CrewFitnessSuite['status']>> = {}): NonNullable<CrewFitnessSuite['status']> {
	return {
		phase: 'Running',
		startedAt: '2026-10-06T10:00:00Z',
		iterationsTotal: 10,
		iterationsCompleted: 2,
		...over
	};
}

describe('meanDurationMs', () => {
	it('averages positive durations', () => {
		expect(meanDurationMs([1000, 3000])).toBe(2000);
	});
	it('ignores zero, negative, and non-finite values', () => {
		expect(meanDurationMs([0, -5, Number.NaN, Infinity, 4000])).toBe(4000);
	});
	it('is 0 for an empty list', () => {
		expect(meanDurationMs([])).toBe(0);
	});
});

describe('suiteEta', () => {
	it('multiplies remaining iterations by elapsed time per completed iteration', () => {
		const eta = suiteEta(status(), T0 + 2 * HOUR);
		expect(eta?.meanMs).toBe(HOUR);
		expect(eta?.remainingMs).toBe(8 * HOUR);
		expect(eta?.finishAtMs).toBe(T0 + 10 * HOUR);
	});

	it('is hidden with zero completed iterations', () => {
		expect(suiteEta(status({ iterationsCompleted: 0 }), T0 + HOUR)).toBeUndefined();
		expect(suiteEta(status({ iterationsCompleted: undefined }), T0 + HOUR)).toBeUndefined();
	});

	it('reports zero remaining when all iterations are completed but the suite still runs', () => {
		const eta = suiteEta(status({ iterationsCompleted: 10 }), T0 + HOUR);
		expect(eta?.remainingMs).toBe(0);
		expect(eta?.finishAtMs).toBe(T0 + HOUR);
	});

	it('never goes negative when more are completed than total', () => {
		expect(suiteEta(status({ iterationsCompleted: 12 }), T0 + HOUR)?.remainingMs).toBe(0);
	});

	it('is gone once the suite finishes', () => {
		expect(suiteEta(status({ completedAt: '2026-10-06T12:00:00Z' }), T0 + HOUR)).toBeUndefined();
		expect(suiteEta(status({ phase: 'Completed' }), T0 + HOUR)).toBeUndefined();
		expect(suiteEta(status({ phase: 'Cancelled' }), T0 + HOUR)).toBeUndefined();
	});

	it('is hidden without a status or total', () => {
		expect(suiteEta(undefined, T0)).toBeUndefined();
		expect(suiteEta(status({ iterationsTotal: 0 }), T0 + HOUR)).toBeUndefined();
	});

	it('needs a usable start time', () => {
		expect(suiteEta(status({ startedAt: undefined }), T0 + HOUR)).toBeUndefined();
		expect(suiteEta(status({ startedAt: 'garbage' }), T0 + HOUR)).toBeUndefined();
	});

	it('is hidden when the browser clock is before the start ', () => {
		expect(suiteEta(status(), T0 - HOUR)).toBeUndefined();
	});

	it('still estimates from durations when the start time is missing', () => {
		const eta = suiteEta(status({ startedAt: undefined }), T0);
		expect(eta?.remainingMs).toBe(8 * 60_000);
	});

	it('treats negative or fractional counts as unusable or floors them', () => {
		expect(suiteEta(status({ iterationsCompleted: -3 }), T0 + HOUR)).toBeUndefined();
		expect(suiteEta(status({ iterationsTotal: 10.9, iterationsCompleted: 2.5 }), T0 + 2000)?.remainingMs).toBe(8000);
	});
});

describe('fmtRemaining', () => {
	it('formats hours and minutes', () => {
		expect(fmtRemaining(80 * 60_000)).toBe('1h 20m');
		expect(fmtRemaining(5 * 60_000)).toBe('5m');
		expect(fmtRemaining(2 * HOUR)).toBe('2h 0m');
	});
	it('shows under 1m for tiny or zero values', () => {
		expect(fmtRemaining(0)).toBe('under 1m');
		expect(fmtRemaining(20_000)).toBe('under 1m');
	});
});

describe('etaText', () => {
	it('labels the text as an estimate with remaining time and a clock time', () => {
		const text = etaText({ remainingMs: 80 * 60_000, finishAtMs: T0, meanMs: 1 });
		expect(text).toMatch(/^~1h 20m left, finish ~.+ \(estimate\)$/);
	});
});
