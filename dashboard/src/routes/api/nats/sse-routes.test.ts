import { beforeEach, describe, expect, it, vi } from 'vitest';

const { getNatsConnection } = vi.hoisted(() => ({
	getNatsConnection: vi.fn()
}));
vi.mock('$lib/server/nats-client', () => ({
	getNatsConnection,
	sc: { decode: (data: Uint8Array) => new TextDecoder().decode(data) }
}));

import { GET as subscribeGET } from './subscribe/+server';
import { GET as streamGET } from './stream/+server';

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** An async-iterable NATS source (subscription or consumer messages) driven by the test. */
function fakeSource(items: { subject: string; data: Uint8Array; seq?: number }[]) {
	let release: () => void = () => {};
	const stopped = new Promise<void>((resolve) => (release = resolve));
	return {
		unsubscribe: vi.fn(() => release()),
		close: vi.fn(async () => release()),
		async *[Symbol.asyncIterator]() {
			yield* items;
			await stopped;
		}
	};
}

type Handler = (event: { url: URL }) => Response | Promise<Response>;

async function open(handler: Handler, query: string) {
	const response = await handler({
		url: new URL(`http://dashboard/api${query}`)
	});
	return (response.body as ReadableStream<Uint8Array>).getReader();
}

async function readEvent(reader: ReadableStreamDefaultReader<Uint8Array>) {
	const { value } = await reader.read();
	return JSON.parse(decoder.decode(value).replace(/^data: /, ''));
}

beforeEach(() => {
	getNatsConnection.mockReset();
});

describe('GET /api/nats/subscribe', () => {
	it('relays messages and unsubscribes when the browser disconnects', async () => {
		const sub = fakeSource([{ subject: 'kubemoot.chat.a', data: encoder.encode('hello') }]);
		const subscribe = vi.fn(() => sub);
		getNatsConnection.mockResolvedValue({ subscribe });

		const reader = await open(
			subscribeGET as unknown as Handler,
			'/nats/subscribe?subject=kubemoot.chat.>'
		);

		expect(await readEvent(reader)).toEqual({
			type: 'connected',
			subject: 'kubemoot.chat.>'
		});
		expect(await readEvent(reader)).toMatchObject({
			type: 'message',
			subject: 'kubemoot.chat.a',
			data: 'hello'
		});
		expect(subscribe).toHaveBeenCalledWith('kubemoot.chat.>');

		await reader.cancel('browser closed the tab');

		expect(sub.unsubscribe).toHaveBeenCalledTimes(1);
	});

	it('sends an error event when NATS is unreachable', async () => {
		getNatsConnection.mockRejectedValue(new Error('connection refused'));
		const reader = await open(subscribeGET as unknown as Handler, '/nats/subscribe');
		expect(await readEvent(reader)).toEqual({
			type: 'error',
			error: 'connection refused'
		});
	});
});

describe('GET /api/nats/stream', () => {
	function fakeJetStream(
		messages: ReturnType<typeof fakeSource>,
		streamExists = true,
		consume: () => Promise<unknown> = async () => messages
	) {
		const jsm = {
			streams: {
				info: vi.fn(() => (streamExists ? Promise.resolve({}) : Promise.reject(new Error('404'))))
			},
			consumers: {
				add: vi.fn(async () => ({ name: 'eph-1' })),
				delete: vi.fn(async () => true)
			}
		};
		const nc = {
			isClosed: () => false,
			jetstreamManager: async () => jsm,
			jetstream: () => ({
				consumers: { get: async () => ({ consume }) }
			})
		};
		getNatsConnection.mockResolvedValue(nc);
		return jsm;
	}

	it('stops and deletes the consumer when the browser disconnects', async () => {
		const messages = fakeSource([
			{
				subject: 'kubemoot.discuss.ns.crew.general.t1',
				data: encoder.encode('{}'),
				seq: 7
			}
		]);
		const jsm = fakeJetStream(messages);

		const reader = await open(
			streamGET as unknown as Handler,
			'/nats/stream?stream=KUBEMOOT_DISCUSS&subject=kubemoot.discuss.>&from_seq=6'
		);

		expect(await readEvent(reader)).toEqual({
			type: 'connected',
			stream: 'KUBEMOOT_DISCUSS',
			subject: 'kubemoot.discuss.>',
			fromSeq: 6
		});
		expect(await readEvent(reader)).toMatchObject({ type: 'message', seq: 7 });
		expect(jsm.consumers.add).toHaveBeenCalledWith(
			'KUBEMOOT_DISCUSS',
			expect.objectContaining({
				filter_subject: 'kubemoot.discuss.>',
				opt_start_seq: 7
			})
		);

		await reader.cancel('browser closed the tab');

		expect(messages.close).toHaveBeenCalledTimes(1);
		expect(jsm.consumers.delete).toHaveBeenCalledWith('KUBEMOOT_DISCUSS', 'eph-1');
	});

	it('replays from the start when no sequence is given', async () => {
		const jsm = fakeJetStream(fakeSource([]));
		const reader = await open(streamGET as unknown as Handler, '/nats/stream?from_seq=junk');

		expect(await readEvent(reader)).toMatchObject({
			type: 'connected',
			fromSeq: 0
		});
		const config = (jsm.consumers.add.mock.calls[0] as unknown[])[1] as Record<string, unknown>;
		expect(config).not.toHaveProperty('opt_start_seq');
		await reader.cancel();
	});

	it('deletes the consumer when it cannot start consuming', async () => {
		const jsm = fakeJetStream(fakeSource([]), true, () =>
			Promise.reject(new Error('consumer not ready'))
		);
		const reader = await open(streamGET as unknown as Handler, '/nats/stream');

		expect(await readEvent(reader)).toEqual({ type: 'error', error: 'consumer not ready' });
		expect(jsm.consumers.delete).toHaveBeenCalledWith('KUBEMOOT_DISCUSS', 'eph-1');
	});

	it('reports a missing stream without creating a consumer', async () => {
		const jsm = fakeJetStream(fakeSource([]), false);
		const reader = await open(streamGET as unknown as Handler, '/nats/stream?stream=NOPE');

		expect(await readEvent(reader)).toEqual({
			type: 'error',
			error: 'Stream NOPE not found'
		});
		expect((await reader.read()).done).toBe(true);
		expect(jsm.consumers.add).not.toHaveBeenCalled();
	});
});
