import { afterEach, describe, expect, it, vi } from 'vitest';
import { relayToSse, sseResponse, sseStream, SSE_HEADERS, type SseSink } from './sse';

const decoder = new TextDecoder();

async function readFrame(reader: ReadableStreamDefaultReader<Uint8Array>): Promise<string> {
	const { value } = await reader.read();
	return value ? decoder.decode(value) : '';
}

/** A source that yields the pushed items until it is ended. */
function pushSource<T>() {
	const queue: T[] = [];
	let wake: (() => void) | undefined;
	let done = false;
	const source: AsyncIterable<T> = {
		async *[Symbol.asyncIterator]() {
			while (!done) {
				if (queue.length === 0) {
					await new Promise<void>((resolve) => (wake = resolve));
					continue;
				}
				yield queue.shift() as T;
			}
		}
	};
	return {
		source,
		push(item: T) {
			queue.push(item);
			wake?.();
		},
		end() {
			done = true;
			wake?.();
		}
	};
}

async function* fromArray<T>(items: T[]): AsyncGenerator<T> {
	yield* items;
}

afterEach(() => {
	vi.useRealTimers();
});

describe('sseStream', () => {
	it('runs the cleanup once when the browser disconnects', async () => {
		const cleanup = vi.fn();
		const stream = sseStream(async (sink) => {
			sink.data({ type: 'connected' });
			return cleanup;
		});
		const reader = stream.getReader();
		expect(await readFrame(reader)).toBe('data: {"type":"connected"}\n\n');

		await reader.cancel('client went away');
		await reader.cancel('again');

		expect(cleanup).toHaveBeenCalledTimes(1);
	});

	it('releases the source when the browser disconnects while it is still opening', async () => {
		const cleanup = vi.fn();
		let finishOpen: () => void = () => {};
		const opened = new Promise<void>((resolve) => (finishOpen = resolve));
		const stream = sseStream(async () => {
			await opened;
			return cleanup;
		});
		const reader = stream.getReader();
		const cancelled = reader.cancel();

		finishOpen();
		await cancelled;
		await vi.waitFor(() => expect(cleanup).toHaveBeenCalledTimes(1));
	});

	it('runs the cleanup when the route closes the stream', async () => {
		const cleanup = vi.fn();
		let sinkRef: SseSink | undefined;
		const stream = sseStream(async (sink) => {
			sinkRef = sink;
			return cleanup;
		});
		const reader = stream.getReader();
		await vi.waitFor(() => expect(sinkRef).toBeDefined());

		sinkRef?.close();

		expect((await reader.read()).done).toBe(true);
		expect(cleanup).toHaveBeenCalledTimes(1);
		expect(sinkRef?.closed).toBe(true);
		expect(sinkRef?.data({ late: true })).toBe(false);
	});

	it('sends an error event and closes when the source fails to open', async () => {
		const reader = sseStream(async () => {
			throw new Error('NATS unreachable');
		}).getReader();

		expect(await readFrame(reader)).toBe('data: {"type":"error","error":"NATS unreachable"}\n\n');
		expect((await reader.read()).done).toBe(true);
	});

	it('sends heartbeats until the stream ends', async () => {
		vi.useFakeTimers();
		const reader = sseStream(async () => undefined, {
			heartbeatMs: 1000
		}).getReader();
		await vi.advanceTimersByTimeAsync(1000);

		expect(await readFrame(reader)).toBe(': heartbeat\n\n');
		await reader.cancel();
		expect(vi.getTimerCount()).toBe(0);
	});

	it('keeps running when the cleanup itself fails', async () => {
		const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
		const reader = sseStream(async () => () => {
			throw new Error('consumer already gone');
		}).getReader();
		await vi.waitFor(async () => {
			await reader.cancel();
			expect(warn).toHaveBeenCalled();
		});
		warn.mockRestore();
	});
});

describe('sseResponse', () => {
	it('serves the stream as text/event-stream', () => {
		const response = sseResponse(async () => undefined);
		expect(response.headers.get('Content-Type')).toBe(SSE_HEADERS['Content-Type']);
		expect(response.headers.get('Cache-Control')).toBe('no-cache');
	});
});

describe('relayToSse', () => {
	function recordingSink() {
		const events: unknown[] = [];
		const sink = {
			closed: false,
			send: vi.fn(() => true),
			data: vi.fn((payload: unknown) => {
				events.push(payload);
				return true;
			}),
			close: vi.fn(() => {
				sink.closed = true;
			})
		};
		return { sink, events };
	}

	it('relays each item and closes the stream when the source ends', async () => {
		const { sink, events } = recordingSink();
		const feed = pushSource<number>();
		const relay = relayToSse(feed.source, sink, (n) => ({ n }));

		feed.push(1);
		feed.push(2);
		await vi.waitFor(() => expect(events).toHaveLength(2));
		feed.end();
		await relay;

		expect(events).toEqual([{ n: 1 }, { n: 2 }]);
		expect(sink.close).toHaveBeenCalledTimes(1);
	});

	it('skips an item it cannot convert', async () => {
		const { sink, events } = recordingSink();
		await relayToSse(fromArray([1, 2, 3]), sink, (n) => {
			if (n === 2) throw new Error('malformed');
			return n;
		});
		expect(events).toEqual([1, 3]);
	});

	it('stops reading once the stream has ended', async () => {
		const { sink, events } = recordingSink();
		sink.closed = true;
		await relayToSse(fromArray([1, 2]), sink, (n) => n);
		expect(events).toEqual([]);
	});

	it('closes the stream when the source fails', async () => {
		const { sink } = recordingSink();
		const failing: AsyncIterable<number> = {
			[Symbol.asyncIterator]: () => ({
				next: () => Promise.reject(new Error('stopped'))
			})
		};
		await relayToSse(failing, sink, (n) => n);
		expect(sink.close).toHaveBeenCalledTimes(1);
	});
});
