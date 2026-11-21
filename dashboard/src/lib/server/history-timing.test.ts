import { afterEach, describe, expect, it, vi } from 'vitest';
import { historyReadSummary, recordHistoryRead, resetHistoryReads, serverTimingHeader, type HistoryTiming } from './history-timing';

const read = (over: Partial<HistoryTiming> = {}): HistoryTiming => ({
	totalMs: 40,
	setupMs: 10,
	fetchMs: 30,
	messages: 200,
	bytes: 50_000,
	scope: 'stream',
	...over
});

afterEach(() => {
	resetHistoryReads();
	vi.restoreAllMocks();
});

describe('history read timing', () => {
	it('has no summary before the first read', () => expect(historyReadSummary()).toBeNull());

	it('summarizes the last read and the average and slowest of them', () => {
		vi.spyOn(console, 'info').mockImplementation(() => {});
		recordHistoryRead(read({ totalMs: 10 }));
		recordHistoryRead(read({ totalMs: 30, messages: 5, bytes: 900 }));
		expect(historyReadSummary()).toEqual({ count: 2, lastMs: 30, avgMs: 20, maxMs: 30, lastBytes: 900, lastMessages: 5 });
	});

	it('keeps the last 100 reads', () => {
		vi.spyOn(console, 'info').mockImplementation(() => {});
		for (let i = 0; i < 130; i++) recordHistoryRead(read());
		expect(historyReadSummary()?.count).toBe(100);
	});

	it('logs durations, size and count and nothing else', () => {
		const info = vi.spyOn(console, 'info').mockImplementation(() => {});
		recordHistoryRead(read({ scope: 'thread' }));
		expect(info).toHaveBeenCalledWith('nats-history scope=thread messages=200 bytes=50000 setup_ms=10 fetch_ms=30 total_ms=40');
	});

	it('writes the Server-Timing header', () => {
		expect(serverTimingHeader(read())).toBe('setup;dur=10, fetch;dur=30, total;dur=40');
	});
});
