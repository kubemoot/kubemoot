import { describe, expect, it } from 'vitest';
import { failureReasonOf, judgeScoreText, scenarioFailures, scoreBandOf, suiteFailureText, GATES_LEGEND } from './fitness-gates';
import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';

type S = Pick<CrewFitnessSuite, 'status'>;

const cond = (type: string, message: string) => ({
	type,
	status: 'False' as const,
	reason: 'r',
	message,
	lastTransitionTime: '2026-11-23T00:00:00Z'
});

describe('judgeScoreText', () => {
	it('is a dash until the judge has scored a scenario', () => {
		expect(judgeScoreText({})).toBe('-');
		expect(judgeScoreText({ status: { judge: { phase: 'Running' } } })).toBe('-');
	});
	it('shows the mean, including zero', () => {
		expect(judgeScoreText({ status: { judge: { mean: 20 } } })).toBe('20');
		expect(judgeScoreText({ status: { judge: { mean: 0 } } })).toBe('0');
	});
});

describe('scoreBandOf', () => {
	it('bands at 70 and 40', () => {
		expect([100, 70, 69, 40, 39, 0].map(scoreBandOf)).toEqual(['ok', 'ok', 'mid', 'mid', 'bad', 'bad']);
	});
});

describe('suiteFailureText', () => {
	it('reads status.error for an Error suite', () => {
		const s: S = { status: { phase: 'Error', error: 'spec.scripts is empty' } };
		expect(suiteFailureText(s)).toBe('spec.scripts is empty');
	});
	it('reads the ToolSmoke condition message for ToolSmokeFailed', () => {
		const s: S = {
			status: {
				phase: 'ToolSmokeFailed',
				conditions: [cond('Other', 'no'), cond('ToolSmoke', '9 tool(s) failed the smoke, no model ran')]
			}
		};
		expect(suiteFailureText(s)).toBe('9 tool(s) failed the smoke, no model ran');
	});
	it('falls back to status.error, then to nothing', () => {
		expect(suiteFailureText({ status: { phase: 'ToolSmokeFailed', error: 'boom' } })).toBe('boom');
		expect(suiteFailureText({ status: { phase: 'ToolSmokeFailed' } })).toBeUndefined();
		expect(suiteFailureText({ status: { phase: 'Error' } })).toBeUndefined();
	});
	it('is empty for suites that executed', () => {
		expect(suiteFailureText({ status: { phase: 'Completed', error: 'stale' } })).toBeUndefined();
		expect(suiteFailureText({})).toBeUndefined();
	});
});

describe('scenario failure reasons', () => {
	const s: S = {
		status: {
			scenarios: [
				{ name: 'a' },
				{ name: 'b', failureReason: 'missing term "nodes"' },
				{ name: 'c', failureReason: '' }
			]
		}
	};
	it('lists only scenarios with a reason', () => {
		expect(scenarioFailures(s)).toEqual([{ name: 'b', reason: 'missing term "nodes"' }]);
		expect(scenarioFailures({})).toEqual([]);
	});
	it('looks a reason up by scenario name', () => {
		expect(failureReasonOf(s, 'b')).toBe('missing term "nodes"');
		expect(failureReasonOf(s, 'a')).toBeUndefined();
		expect(failureReasonOf(s, 'c')).toBeUndefined();
		expect(failureReasonOf(s, 'nope')).toBeUndefined();
	});
});

describe('GATES_LEGEND', () => {
	it('is ASCII and names both results', () => {
		expect(GATES_LEGEND.join(' ')).toMatch(/^[\x20-\x7e]+$/);
		expect(GATES_LEGEND[0]).toContain('Gates');
		expect(GATES_LEGEND[1]).toContain('Judge score');
	});
});
