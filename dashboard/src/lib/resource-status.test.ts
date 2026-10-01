import { describe, expect, it } from 'vitest';
import {
	NO_VALUE,
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
