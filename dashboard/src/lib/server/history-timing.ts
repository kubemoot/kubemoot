// What reading discussion history costs, kept in memory and content-free: durations,
// sizes and counts only, never a subject, a thread id or a message.
import type { HistoryReadSummary } from '#lib/nats/status.js';

/** One history read as measured on the server. */
export interface HistoryTiming {
	/** Whole handler time. */
	totalMs: number;
	/** Time to the stream's info and the consumer, before any message is fetched. */
	setupMs: number;
	/** Time fetching and decoding messages. */
	fetchMs: number;
	messages: number;
	/** Size of the JSON response body in bytes. */
	bytes: number;
	/** 'thread' when the read was one thread's subject, 'stream' for the stream's newest messages. */
	scope: 'thread' | 'stream';
}

const KEEP = 100;
const recent: HistoryTiming[] = [];

export function recordHistoryRead(t: HistoryTiming): void {
	recent.push(t);
	if (recent.length > KEEP) recent.shift();
	console.info(
		`nats-history scope=${t.scope} messages=${t.messages} bytes=${t.bytes} setup_ms=${t.setupMs} fetch_ms=${t.fetchMs} total_ms=${t.totalMs}`
	);
}

/** The Server-Timing header value for one read. */
export function serverTimingHeader(t: HistoryTiming): string {
	return `setup;dur=${t.setupMs}, fetch;dur=${t.fetchMs}, total;dur=${t.totalMs}`;
}

/** The last reads in summary; null before the first read. */
export function historyReadSummary(): HistoryReadSummary | null {
	const last = recent.at(-1);
	if (!last) return null;
	const total = recent.reduce((n, t) => n + t.totalMs, 0);
	return {
		count: recent.length,
		lastMs: last.totalMs,
		avgMs: Math.round(total / recent.length),
		maxMs: Math.max(...recent.map((t) => t.totalMs)),
		lastBytes: last.bytes,
		lastMessages: last.messages
	};
}

export function resetHistoryReads(): void {
	recent.length = 0;
}
