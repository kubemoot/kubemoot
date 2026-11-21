import { describe, expect, it } from 'vitest';
import { retentionLine, storeRow } from './view';
import type { StoreStatus } from './status';

const base = (share?: 'cluster' | 'namespaces'): StoreStatus => ({
	definition: null,
	name: 'KUBEMOOT_DISCUSS',
	kind: 'stream',
	share,
	limits: { maxAgeSeconds: null, maxBytes: 1000, maxMessages: null, history: null },
	usage: { messages: 5, bytes: 500, firstSeq: 3, lastSeq: 9, consumers: 2, keys: null }
});

describe('storeRow on a scoped dashboard', () => {
	it('shows cluster figures for a whole store', () => {
		const r = storeRow(base());
		expect([r.sequences, r.consumers, r.bytesShare]).toEqual(['3 to 9', '2', '50%']);
	});

	it('hides what is cluster-wide for a share', () => {
		const r = storeRow(base('namespaces'));
		expect([r.sequences, r.consumers, r.bytesShare]).toEqual(['not shown', '', '']);
	});
});

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
