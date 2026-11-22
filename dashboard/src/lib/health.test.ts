import { describe, expect, it } from 'vitest';
import { healthBadge, healthLabel, needsAttention, readyHealth, worstHealth, type Health } from './health';

describe('worstHealth', () => {
	it('takes the worst state beneath it', () => {
		expect(worstHealth(['healthy', 'degraded', 'idle'])).toBe('degraded');
		expect(worstHealth(['healthy', 'down', 'degraded'])).toBe('down');
		expect(worstHealth(['healthy', 'unknown'])).toBe('unknown');
	});

	it('ranks down over degraded over unknown over healthy over idle', () => {
		const order: Health[] = ['down', 'degraded', 'unknown', 'healthy', 'idle'];
		for (let i = 0; i < order.length - 1; i++) expect(worstHealth([order[i + 1], order[i]])).toBe(order[i]);
	});

	it('is idle when nothing is there: nothing runs and nothing is wrong', () => {
		expect(worstHealth([])).toBe('idle');
		expect(worstHealth(['idle', 'idle'])).toBe('idle');
	});

	it('is healthy when something works and the rest is idle', () => {
		expect(worstHealth(['idle', 'healthy'])).toBe('healthy');
	});
});

describe('needsAttention', () => {
	it('is true for down and degraded only', () => {
		expect((['down', 'degraded', 'unknown', 'healthy', 'idle'] as Health[]).filter(needsAttention)).toEqual(['down', 'degraded']);
	});
});

describe('labels and badges', () => {
	it('names the five states and draws each', () => {
		expect((['healthy', 'degraded', 'down', 'idle', 'unknown'] as Health[]).map(healthLabel)).toEqual(['Healthy', 'Degraded', 'Down', 'Idle', 'Unknown']);
		expect(healthBadge('healthy')).toBe('success');
		expect(healthBadge('degraded')).toBe('warning');
		expect(healthBadge('down')).toBe('error');
		expect(healthBadge('idle')).toBe('unknown');
	});
});

describe('readyHealth', () => {
	it('is healthy when ready, down in a failure phase, degraded otherwise', () => {
		expect(readyHealth({ ready: true })).toBe('healthy');
		expect(readyHealth({ ready: false, phase: 'Error' })).toBe('down');
		expect(readyHealth({ ready: false, phase: 'Failed' })).toBe('down');
		expect(readyHealth({ ready: false, phase: 'Pending' })).toBe('degraded');
		expect(readyHealth({})).toBe('degraded');
		expect(readyHealth(undefined)).toBe('degraded');
	});

	it('takes the failure phases of the resource', () => {
		expect(readyHealth({ phase: 'Broken' }, ['Broken'])).toBe('down');
		expect(readyHealth({ phase: 'Failed' }, ['Broken'])).toBe('degraded');
	});
});
