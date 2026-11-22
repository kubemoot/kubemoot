// @vitest-environment node
import type { KvEntry } from 'nats';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const guardKvRead = vi.fn();
const keyNamespaceAllowed = vi.fn();
let held: Record<string, unknown> = {};
let reportsInitialized = true;

vi.mock('#lib/server/scope.js', () => ({ guardKvRead, keyNamespaceAllowed }));
vi.mock('#lib/server/nats-client.js', async () => {
	const { StringCodec } = await import('nats');
	const sc = StringCodec();
	const kv = {
		async watch({ initializedFn }: { initializedFn: () => void }) {
			let stopped = false;
			return {
				stop: () => {
					stopped = true;
				},
				async *[Symbol.asyncIterator]() {
					for (const [key, value] of Object.entries(held)) {
						yield { key, operation: 'PUT', value: sc.encode(JSON.stringify(value)) } as unknown as KvEntry;
					}
					if (reportsInitialized) initializedFn();
					// the next heartbeat is far away: a reader that waits for it hangs
					while (!stopped) await new Promise((r) => setTimeout(r, 5));
				}
			};
		}
	};
	return {
		sc,
		getNatsConnection: async () => ({ jetstream: () => ({ views: { kv: async () => kv } }) })
	};
});

const { GET } = await import('./+server.js');

const call = (query = '') => GET({ url: new URL(`https://dash.test/api/nats/kv${query}`) } as Parameters<typeof GET>[0]);

beforeEach(() => {
	guardKvRead.mockReset().mockResolvedValue(null);
	keyNamespaceAllowed.mockReset().mockReturnValue(true);
	held = {};
	reportsInitialized = true;
});

describe('GET /api/nats/kv', () => {
	it('answers within a second with the heartbeats the bucket holds, not after the next heartbeat', async () => {
		held = { 'crew-a.meta': { state: 'idle' }, 'crew-a.triage': { state: 'busy' } };
		const started = Date.now();
		const res = await call();
		expect(Date.now() - started).toBeLessThan(1000);
		expect(await res.json()).toEqual({ agents: held });
	});

	it('answers at once for an empty bucket', async () => {
		const started = Date.now();
		const res = await call();
		expect(Date.now() - started).toBeLessThan(1000);
		expect(await res.json()).toEqual({ agents: {} });
	});

	it('answers within its deadline, marked partial, when the bucket never reports it is done', async () => {
		held = { 'crew-a.meta': { state: 'idle' } };
		reportsInitialized = false;
		const started = Date.now();
		const res = await call();
		expect(Date.now() - started).toBeLessThan(5000);
		expect(await res.json()).toEqual({ agents: held, partial: true });
	}, 10_000);

	it('leaves out keys of a namespace the person may not see', async () => {
		held = { 'crew-a.meta': { state: 'idle' }, 'crew-b.meta': { state: 'idle' } };
		keyNamespaceAllowed.mockImplementation((key: string) => key.startsWith('crew-a.'));
		expect(Object.keys((await (await call()).json()).agents)).toEqual(['crew-a.meta']);
	});

	it('returns the guard refusal and reads nothing for a bucket that is not served', async () => {
		guardKvRead.mockResolvedValue(new Response('{"error":"bucket"}', { status: 403 }));
		expect((await call('?bucket=other')).status).toBe(403);
	});
});
