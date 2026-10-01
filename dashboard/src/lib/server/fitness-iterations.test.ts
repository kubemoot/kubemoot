import { describe, expect, it } from 'vitest';
import {
	isIterationKey,
	iterationStatus,
	parseIterationKey,
	scenarioLabel,
	summarizeTranscript
} from './fitness-iterations';

describe('isIterationKey and parseIterationKey', () => {
	it('accept a per-iteration transcript name', () => {
		expect(isIterationKey('s2-i14.json')).toBe(true);
		expect(parseIterationKey('s2-i14.json')).toEqual({ scriptIdx: 2, iter: 14 });
	});

	it('reject sidecars and malformed names', () => {
		for (const tail of ['judge-v1.json', 'report.xlsx', 's1-i2.json.bak', 'sX-i1.json', '']) {
			expect(isIterationKey(tail)).toBe(false);
			expect(parseIterationKey(tail)).toEqual({ scriptIdx: -1, iter: -1 });
		}
	});
});

describe('scenarioLabel', () => {
	const scripts = [{ testRef: 'gpu-inventory' }, {}, { testRef: '' }];

	it('uses the script testRef', () => {
		expect(scenarioLabel(scripts, 0)).toBe('gpu-inventory');
	});

	it('falls back to the script index without a testRef or script', () => {
		expect(scenarioLabel(scripts, 1)).toBe('script 1');
		expect(scenarioLabel(scripts, 2)).toBe('script 2');
		expect(scenarioLabel(scripts, 9)).toBe('script 9');
		expect(scenarioLabel(scripts, -1)).toBe('script -1');
	});
});

describe('iterationStatus', () => {
	it('is Unknown without assertions, Passed when all pass, Failed otherwise', () => {
		expect(iterationStatus(0, 0)).toBe('Unknown');
		expect(iterationStatus(3, 3)).toBe('Passed');
		expect(iterationStatus(2, 3)).toBe('Failed');
		expect(iterationStatus(0, 1)).toBe('Failed');
	});
});

describe('summarizeTranscript', () => {
	it('counts assertions and keeps the duration', () => {
		expect(
			summarizeTranscript({
				assertions: [{ passed: true }, { passed: false }, { passed: true }],
				durationMs: 4200
			})
		).toEqual({ status: 'Failed', assertionsPassed: 2, assertionsTotal: 3, durationMs: 4200 });
	});

	it('gives an Unknown, empty row for a missing or empty transcript', () => {
		const empty = { status: 'Unknown', assertionsPassed: 0, assertionsTotal: 0, durationMs: 0 };
		expect(summarizeTranscript(null)).toEqual(empty);
		expect(summarizeTranscript({})).toEqual(empty);
	});
});
