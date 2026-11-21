import { describe, expect, it } from 'vitest';
import { retentionLine } from './view';

describe('retentionLine', () => {
	it('says when no cleanup has run', () => {
		expect(retentionLine(null)).toBe('No cleanup has run yet');
		expect(retentionLine({})).toBe('No cleanup has run yet');
	});

	it('says when it ran and what it removed', () => {
		const line = retentionLine({ lastRunTime: '2026-11-20T10:00:00Z', removed: { conversations: 3, memory: 1 } });
		expect(line).toBe('2026-11-20T10:00:00Z: removed 3 conversations, 1 facts, 0 other');
	});

	it('says why a run did not finish', () => {
		expect(retentionLine({ lastRunTime: 't', error: 'bucket full' })).toContain('did not finish: bucket full');
	});
});
