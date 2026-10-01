/**
 * Server-sent events over a ReadableStream, with one owner for teardown.
 *
 * A route passes an `open` function that sets up its source (a NATS subscription,
 * a JetStream consumer, a Kubernetes watch) and returns the function that releases
 * it. The session keeps that cleanup in its own scope and runs it exactly once,
 * whichever way the stream ends: the browser disconnects (ReadableStream cancel),
 * the route closes the stream, or an enqueue fails. A disconnect that arrives
 * while `open` is still running releases the source as soon as `open` returns it.
 */

export type SseCleanup = () => void | Promise<void>;

export interface SseSink {
	/** Queues a raw SSE frame; false once the stream has ended. */
	send(frame: string): boolean;
	/** Queues `data: <json>` as one SSE event; false once the stream has ended. */
	data(payload: unknown): boolean;
	/** Ends the stream from the server side and releases the source. */
	close(): void;
	readonly closed: boolean;
}

export type SseOpen = (sink: SseSink) => Promise<SseCleanup | void>;

export interface SseOptions {
	heartbeatMs?: number;
}

export const SSE_HEADERS = {
	'Content-Type': 'text/event-stream',
	'Cache-Control': 'no-cache',
	Connection: 'keep-alive'
} as const;

const DEFAULT_HEARTBEAT_MS = 30_000;
const HEARTBEAT_FRAME = ': heartbeat\n\n';

class SseSession implements SseSink {
	private readonly encoder = new TextEncoder();
	private cleanup: SseCleanup | undefined;
	private heartbeat: ReturnType<typeof setInterval> | undefined;
	private ended = false;

	constructor(private readonly controller: ReadableStreamDefaultController<Uint8Array>) {}

	get closed(): boolean {
		return this.ended;
	}

	async open(open: SseOpen, heartbeatMs: number): Promise<void> {
		try {
			this.cleanup = (await open(this)) ?? undefined;
		} catch (err) {
			this.data({
				type: 'error',
				error: err instanceof Error ? err.message : String(err)
			});
			this.close();
			return;
		}
		if (this.ended) {
			await this.release();
			return;
		}
		this.heartbeat = setInterval(() => this.send(HEARTBEAT_FRAME), heartbeatMs);
	}

	send(frame: string): boolean {
		if (this.ended) return false;
		try {
			this.controller.enqueue(this.encoder.encode(frame));
			return true;
		} catch {
			void this.end();
			return false;
		}
	}

	data(payload: unknown): boolean {
		return this.send(`data: ${JSON.stringify(payload)}\n\n`);
	}

	close(): void {
		if (this.ended) return;
		void this.end();
		try {
			this.controller.close();
		} catch {
			/* the stream was already closed or errored */
		}
	}

	async end(): Promise<void> {
		if (this.ended) return;
		this.ended = true;
		await this.release();
	}

	private async release(): Promise<void> {
		clearInterval(this.heartbeat);
		const cleanup = this.cleanup;
		this.cleanup = undefined;
		try {
			await cleanup?.();
		} catch (err) {
			console.warn('SSE source cleanup failed:', err);
		}
	}
}

/** A byte stream of SSE frames whose source `open` sets up and whose end releases it. */
export function sseStream(open: SseOpen, options: SseOptions = {}): ReadableStream<Uint8Array> {
	let session: SseSession | undefined;
	return new ReadableStream<Uint8Array>({
		start(controller) {
			session = new SseSession(controller);
			return session.open(open, options.heartbeatMs ?? DEFAULT_HEARTBEAT_MS);
		},
		cancel() {
			return session?.end();
		}
	});
}

export function sseResponse(open: SseOpen, options: SseOptions = {}): Response {
	return new Response(sseStream(open, options), { headers: SSE_HEADERS });
}

/**
 * Relays each item of `source` to the sink as one data event until either side
 * ends; an item `toEvent` cannot convert is skipped. When the source ends first,
 * the stream closes so the browser reconnects instead of waiting on a dead source.
 */
export async function relayToSse<T>(
	source: AsyncIterable<T>,
	sink: SseSink,
	toEvent: (item: T) => unknown
): Promise<void> {
	try {
		for await (const item of source) {
			if (sink.closed) break;
			sendConverted(sink, item, toEvent);
		}
	} catch (err) {
		console.debug('SSE source stopped with an error:', err);
	} finally {
		sink.close();
	}
}

function sendConverted<T>(sink: SseSink, item: T, toEvent: (item: T) => unknown): void {
	let event: unknown;
	try {
		event = toEvent(item);
	} catch {
		return;
	}
	sink.data(event);
}
