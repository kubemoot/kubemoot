import { describe, expect, it } from 'vitest';
import { statusIcon } from './thread-status-icon.js';

describe('statusIcon', () => {
	it('names each state', () => {
		expect(statusIcon('working').label).toBe('Active');
		expect(statusIcon('answered').label).toBe('Answered');
		expect(statusIcon('failed').label).toBe('Failed, no answer');
		expect(statusIcon('stopped').label).toBe('Stopped');
	});
	it('carries the crew version without the build suffix in the tooltip', () => {
		const t = statusIcon('answered', { crewVersion: '0.51.0-rc.2_5776d8b4e1a8', startedBy: 'human' }).tooltip;
		expect(t).toContain('Crew version 0.51.0-rc.2');
		expect(t).toContain('Started by a person');
	});
	it('is just the label without a version', () => {
		expect(statusIcon('working', {}).tooltip).toBe('Active');
	});
});
