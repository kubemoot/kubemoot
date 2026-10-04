import { describe, expect, it } from 'vitest';
import {
	availableSuiteActions,
	suiteActionPatch,
	suiteDisplayPhase,
	suiteIsJudged
} from './fitness-suite-controls';
import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';

type SuiteLike = Pick<CrewFitnessSuite, 'spec' | 'status'>;

function suite(phase: string | undefined, spec: { suspend?: boolean; cancel?: boolean } = {}): SuiteLike {
	return {
		spec: { crewRef: 'c', iterations: 1, scripts: [], ...spec },
		status: phase ? { phase: phase as NonNullable<CrewFitnessSuite['status']>['phase'] } : undefined
	};
}

describe('suiteActionPatch', () => {
	it('maps each action to its spec field', () => {
		expect(suiteActionPatch('pause')).toEqual({ spec: { suspend: true } });
		expect(suiteActionPatch('resume')).toEqual({ spec: { suspend: false } });
		expect(suiteActionPatch('stop')).toEqual({ spec: { cancel: true } });
	});

	it('rejects unknown or malformed actions', () => {
		expect(suiteActionPatch('delete')).toBeNull();
		expect(suiteActionPatch('')).toBeNull();
		expect(suiteActionPatch(undefined)).toBeNull();
		expect(suiteActionPatch({ action: 'pause' })).toBeNull();
	});
});

describe('availableSuiteActions', () => {
	it('offers pause and stop while running', () => {
		expect(availableSuiteActions(suite('Running'))).toEqual(['pause', 'stop']);
		expect(availableSuiteActions(suite('Pending'))).toEqual(['pause', 'stop']);
		expect(availableSuiteActions(suite(undefined))).toEqual(['pause', 'stop']);
	});

	it('offers resume and stop while paused or pausing', () => {
		expect(availableSuiteActions(suite('Paused', { suspend: true }))).toEqual(['resume', 'stop']);
		expect(availableSuiteActions(suite('Running', { suspend: true }))).toEqual(['resume', 'stop']);
	});

	it('offers nothing once terminal or stopping', () => {
		for (const phase of ['Completed', 'Failed', 'Error', 'Cancelled']) {
			expect(availableSuiteActions(suite(phase))).toEqual([]);
		}
		expect(availableSuiteActions(suite('Running', { cancel: true }))).toEqual([]);
	});
});

describe('suiteDisplayPhase', () => {
	it('shows Pausing while a suspended suite drains', () => {
		expect(suiteDisplayPhase(suite('Running', { suspend: true }))).toBe('Pausing');
	});

	it('shows Stopping before the operator applies cancel', () => {
		expect(suiteDisplayPhase(suite('Running', { cancel: true }))).toBe('Stopping');
		expect(suiteDisplayPhase(suite('Paused', { suspend: true, cancel: true }))).toBe('Stopping');
	});

	it('passes the operator phase through otherwise', () => {
		expect(suiteDisplayPhase(suite('Running'))).toBe('Running');
		expect(suiteDisplayPhase(suite('Paused', { suspend: true }))).toBe('Paused');
		expect(suiteDisplayPhase(suite('Cancelled', { cancel: true }))).toBe('Cancelled');
		expect(suiteDisplayPhase(suite('Completed', { suspend: true }))).toBe('Completed');
		expect(suiteDisplayPhase(suite(undefined))).toBeUndefined();
	});
});

describe('suiteIsJudged', () => {
	it('judges finished suites but never cancelled or live ones', () => {
		expect(suiteIsJudged('Completed')).toBe(true);
		expect(suiteIsJudged('Failed')).toBe(true);
		expect(suiteIsJudged('Error')).toBe(true);
		expect(suiteIsJudged('Cancelled')).toBe(false);
		expect(suiteIsJudged('Paused')).toBe(false);
		expect(suiteIsJudged('Running')).toBe(false);
		expect(suiteIsJudged()).toBe(false);
	});
});
