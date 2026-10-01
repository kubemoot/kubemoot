import { beforeEach, describe, expect, it, vi } from 'vitest';

const { watchFn } = vi.hoisted(() => ({ watchFn: vi.fn() }));
vi.mock('@kubernetes/client-node', () => ({
	Watch: class {
		watch = watchFn;
	}
}));
vi.mock('./client.js', () => ({ getKubeConfig: () => ({}) }));

import { crdWatchResponse, isWatchableCrd } from './watch-sse';

const decoder = new TextDecoder();

async function readEvent(reader: ReadableStreamDefaultReader<Uint8Array>) {
	const { value } = await reader.read();
	return JSON.parse(decoder.decode(value).replace(/^data: /, ''));
}

beforeEach(() => {
	watchFn.mockReset();
});

describe('isWatchableCrd', () => {
	it('accepts a kubemoot CRD plural and rejects anything else', () => {
		expect(isWatchableCrd('agents')).toBe(true);
		expect(isWatchableCrd('pods')).toBe(false);
	});
});

describe('crdWatchResponse', () => {
	it('relays watch events and aborts the watch when the browser disconnects', async () => {
		const aborter = new AbortController();
		watchFn.mockImplementation(
			async (_path: string, _q: unknown, onEvent: (t: string, o: unknown) => void) => {
				onEvent('ADDED', { metadata: { name: 'a1' } });
				return aborter;
			}
		);

		const reader = (
			crdWatchResponse(['agents'], 'kubemoot').body as ReadableStream<Uint8Array>
		).getReader();

		expect(await readEvent(reader)).toEqual({
			kind: 'Agent',
			type: 'ADDED',
			object: { metadata: { name: 'a1' } }
		});
		expect(await readEvent(reader)).toEqual({ type: 'synced' });
		expect(watchFn.mock.calls[0][0]).toBe('/apis/kubemoot.ai/v1alpha1/namespaces/kubemoot/agents');

		await reader.cancel();

		expect(aborter.signal.aborted).toBe(true);
	});

	it('restarts a watch that ends and aborts only the live one on disconnect', async () => {
		vi.useFakeTimers();
		const first = new AbortController();
		const second = new AbortController();
		const abortFirst = vi.spyOn(first, 'abort');
		let endWatch: (err: unknown) => void = () => {};
		watchFn
			.mockImplementationOnce(
				async (_p: string, _q: unknown, _e: unknown, onDone: (err: unknown) => void) => {
					endWatch = onDone;
					return first;
				}
			)
			.mockResolvedValueOnce(second);

		const reader = (
			crdWatchResponse(['agents'], 'kubemoot').body as ReadableStream<Uint8Array>
		).getReader();
		expect(await readEvent(reader)).toEqual({ type: 'synced' });

		endWatch(null);
		await vi.advanceTimersByTimeAsync(1000);
		expect(watchFn).toHaveBeenCalledTimes(2);

		await reader.cancel();
		vi.useRealTimers();

		expect(second.signal.aborted).toBe(true);
		expect(abortFirst).not.toHaveBeenCalled();
	});

	it('watches across namespaces and reports a watch that fails to start', async () => {
		watchFn.mockRejectedValue(new Error('forbidden'));

		const reader = (
			crdWatchResponse(['agents'], '').body as ReadableStream<Uint8Array>
		).getReader();

		expect(await readEvent(reader)).toEqual({
			kind: 'Agent',
			type: 'ERROR',
			error: 'forbidden'
		});
		expect(watchFn.mock.calls[0][0]).toBe('/apis/kubemoot.ai/v1alpha1/agents');
		await reader.cancel();
	});
});
