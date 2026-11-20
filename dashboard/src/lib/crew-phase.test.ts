import { describe, expect, it } from 'vitest';
import type { Condition } from '#lib/types/kubemoot.js';
import { WAITING_FOR_CAPACITY, capacityCondition, capacityLabel, crewCanAnswer, crewPhaseLabel } from './crew-phase';

const condition = (type: string, status: Condition['status']): Condition => ({
	type,
	status,
	reason: 'r',
	message: 'm',
	lastTransitionTime: '2026-11-20T00:00:00Z'
});

describe('crewPhaseLabel', () => {
	it('spells out WaitingForCapacity', () => {
		expect(crewPhaseLabel(WAITING_FOR_CAPACITY)).toBe('Waiting for capacity');
	});
	it('leaves the other phases as they are', () => {
		expect(crewPhaseLabel('Ready')).toBe('Ready');
		expect(crewPhaseLabel('Error')).toBe('Error');
	});
	it('names a missing phase Unknown', () => {
		expect(crewPhaseLabel(undefined)).toBe('Unknown');
		expect(crewPhaseLabel('')).toBe('Unknown');
	});
});

describe('crewCanAnswer', () => {
	it('is true for a ready crew with capacity', () => {
		expect(crewCanAnswer('Ready', true)).toBe(true);
	});
	it('is false while the crew waits for capacity, though it is ready', () => {
		expect(crewCanAnswer(WAITING_FOR_CAPACITY, true)).toBe(false);
	});
	it('is false for a crew that is not ready or has no status', () => {
		expect(crewCanAnswer('Pending', false)).toBe(false);
		expect(crewCanAnswer(undefined, undefined)).toBe(false);
	});
});

describe('capacityCondition', () => {
	it('finds the CapacityAvailable condition among the others', () => {
		const found = capacityCondition([condition('Ready', 'True'), condition('CapacityAvailable', 'False')]);
		expect(found?.status).toBe('False');
	});
	it('is undefined when the crew has not been assessed or has no conditions', () => {
		expect(capacityCondition([condition('Ready', 'True')])).toBeUndefined();
		expect(capacityCondition(undefined)).toBeUndefined();
	});
});

describe('capacityLabel', () => {
	it('reads each status', () => {
		expect(capacityLabel(condition('CapacityAvailable', 'True'))).toBe('Available');
		expect(capacityLabel(condition('CapacityAvailable', 'False'))).toBe('Not available');
		expect(capacityLabel(condition('CapacityAvailable', 'Unknown'))).toBe('Unknown');
	});
});
