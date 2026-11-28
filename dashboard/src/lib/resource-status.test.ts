import { describe, expect, it } from 'vitest';
import {
	isModelServerUnavailable,
	NO_VALUE,
	modelServerStatus,
	phaseStatus,
	readinessStatus,
	readyCountStatus,
	verdictStatus,
	yesNo
} from './resource-status';

describe('readinessStatus', () => {
	it('is success when ready, whatever the phase', () => {
		expect(readinessStatus({ ready: true, phase: 'Error' })).toBe('success');
	});

	it('is error only in the failure phase', () => {
		expect(readinessStatus({ phase: 'Error' })).toBe('error');
		expect(readinessStatus({ phase: 'Failed' })).toBe('pending');
		expect(readinessStatus({ phase: 'Failed' }, 'Failed')).toBe('error');
	});

	it('is pending without a status', () => {
		expect(readinessStatus(undefined)).toBe('pending');
		expect(readinessStatus({})).toBe('pending');
	});
});

describe('phaseStatus', () => {
	it('maps Ready, Error, and anything else', () => {
		expect(phaseStatus('Ready')).toBe('success');
		expect(phaseStatus('Error')).toBe('error');
		expect(phaseStatus('Syncing')).toBe('pending');
		expect(phaseStatus(undefined)).toBe('pending');
	});
});

describe('verdictStatus', () => {
	it('maps each verdict and treats anything else as untested', () => {
		expect(verdictStatus('use')).toBe('success');
		expect(verdictStatus('avoid')).toBe('error');
		expect(verdictStatus('caution')).toBe('warning');
		expect(verdictStatus(undefined)).toBe('pending');
		expect(verdictStatus('maybe')).toBe('pending');
	});
});

describe('readyCountStatus', () => {
	it('is unknown with nothing, success when all, warning when some, error when none', () => {
		expect(readyCountStatus(0, 0)).toBe('unknown');
		expect(readyCountStatus(3, 3)).toBe('success');
		expect(readyCountStatus(1, 3)).toBe('warning');
		expect(readyCountStatus(0, 3)).toBe('error');
	});
});

describe('yesNo', () => {
	it('shows Yes, No, or the no-value mark', () => {
		expect(yesNo(true)).toBe('Yes');
		expect(yesNo(false)).toBe('No');
		expect(yesNo(undefined)).toBe(NO_VALUE);
		expect(NO_VALUE.codePointAt(0)).toBe(0x2014);
	});
});

describe('modelServerStatus', () => {
	it('shows Unavailable as neutral, not as an error', () => {
		expect(modelServerStatus({ ready: false, phase: 'Unavailable' })).toBe('unknown');
	});

	it('shows Failed as an error and Ready as success', () => {
		expect(modelServerStatus({ ready: false, phase: 'Failed' })).toBe('error');
		expect(modelServerStatus({ ready: true, phase: 'Ready' })).toBe('success');
	});

	it('leaves any other phase pending', () => {
		expect(modelServerStatus({ ready: false, phase: 'Pending' })).toBe('pending');
		expect(modelServerStatus(undefined)).toBe('pending');
	});
});

describe('isModelServerUnavailable', () => {
	it('is true only for the Unavailable phase while not ready', () => {
		expect(isModelServerUnavailable({ ready: false, phase: 'Unavailable' })).toBe(true);
		expect(isModelServerUnavailable({ ready: true, phase: 'Unavailable' })).toBe(false);
		expect(isModelServerUnavailable({ ready: false, phase: 'Failed' })).toBe(false);
		expect(isModelServerUnavailable(undefined)).toBe(false);
	});
});
