// @vitest-environment node
import type { KvEntry } from 'nats';
import { describe, expect, it } from 'vitest';
import { readKvSnapshot, type KvWatchSource } from './kv-snapshot';

const entry = (key: string): KvEntry => ({ key, operation: 'PUT', value: new Uint8Array([1]) }) as unknown as KvEntry;

interface Script {
	/** Entries the store holds. */
	held: string[];
	/** Whether the store reports that it delivered everything it held. */
	reportsInitialized: boolean;
}

/**
 * A store like the real one: it delivers what it holds, tells when that is done, and then
 * waits for a write that never comes in this test (the next heartbeat, minutes away).
 */
function store({ held, reportsInitialized }: Script): { kv: KvWatchSource; stopped: () => boolean } {
	let stopped = false;
	const kv: KvWatchSource = {
		async watch({ initializedFn }) {
			return {
				stop: () => {
					stopped = true;
				},
				async *[Symbol.asyncIterator]() {
					for (const key of held) yield entry(key);
					if (reportsInitialized) initializedFn();
					await new Promise<void>((resolve) => {
						const poll = setInterval(() => stopped && (clearInterval(poll), resolve()), 5);
					});
				}
			};
		}
	};
	return { kv, stopped: () => stopped };
}

const keep = (e: KvEntry) => e.key;

describe('readKvSnapshot', () => {
	it('returns what the store holds as soon as it says it is done, without waiting for a later write', async () => {
		const { kv, stopped } = store({ held: ['crew.a', 'crew.b'], reportsInitialized: true });
		const started = Date.now();
		const snapshot = await readKvSnapshot(kv, keep, 5000);
		expect(Date.now() - started).toBeLessThan(1000);
		expect(snapshot).toEqual({ values: { 'crew.a': 'crew.a', 'crew.b': 'crew.b' }, complete: true });
		expect(stopped()).toBe(true);
	});

	it('returns at once for an empty store, which never delivers an entry', async () => {
		const { kv } = store({ held: [], reportsInitialized: true });
		const started = Date.now();
		expect(await readKvSnapshot(kv, keep, 5000)).toEqual({ values: {}, complete: true });
		expect(Date.now() - started).toBeLessThan(1000);
	});

	it('ends at the deadline with what arrived when the store never says it is done', async () => {
		const { kv, stopped } = store({ held: ['crew.a'], reportsInitialized: false });
		const started = Date.now();
		const snapshot = await readKvSnapshot(kv, keep, 100);
		expect(Date.now() - started).toBeLessThan(1000);
		expect(snapshot).toEqual({ values: { 'crew.a': 'crew.a' }, complete: false });
		expect(stopped()).toBe(true);
	});

	it('ends at the deadline when the watch cannot even be opened', async () => {
		const kv: KvWatchSource = { watch: () => new Promise(() => undefined) };
		const started = Date.now();
		expect(await readKvSnapshot(kv, keep, 100)).toEqual({ values: {}, complete: false });
		expect(Date.now() - started).toBeLessThan(1000);
	});

	it('leaves out the entries the picker declines', async () => {
		const { kv } = store({ held: ['crew.a', 'skip.b'], reportsInitialized: true });
		const snapshot = await readKvSnapshot(kv, (e) => (e.key.startsWith('skip') ? undefined : 1));
		expect(Object.keys(snapshot.values)).toEqual(['crew.a']);
	});

	it('keeps what arrived when the watch fails midway', async () => {
		const kv: KvWatchSource = {
			async watch() {
				return {
					stop: () => undefined,
					async *[Symbol.asyncIterator]() {
						yield entry('crew.a');
						throw new Error('connection lost');
					}
				};
			}
		};
		const snapshot = await readKvSnapshot(kv, keep, 150);
		expect(snapshot.values).toEqual({ 'crew.a': 'crew.a' });
	});
});
