import type { KvEntry } from 'nats';

/** The part of a NATS key-value store a snapshot needs; the real store and a test double both fit. */
export interface KvWatchSource {
	watch(opts: { initializedFn: () => void }): Promise<AsyncIterable<KvEntry> & { stop(): void }>;
}

/**
 * The longest a snapshot may take. It is a safety net: the normal path ends the moment the
 * store reports it delivered everything it held, which takes milliseconds.
 */
export const KV_SNAPSHOT_DEADLINE_MS = 3000;

export interface KvSnapshot {
	/** What `pick` made of each entry that arrived. */
	values: Record<string, unknown>;
	/** False when the deadline ended the read before the store said it was done. */
	complete: boolean;
}

/**
 * Reads the entries a key-value store holds right now and returns.
 *
 * The store reports "everything held was delivered" through `initializedFn`, which fires after
 * the last held entry and before any later one. A loop that tests a flag only when a new entry
 * arrives waits for the next write (an agent heartbeat, many seconds away; never, for an
 * empty store). So entries are collected on the side and the read ends on that signal.
 */
export async function readKvSnapshot(
	kv: KvWatchSource,
	pick: (entry: KvEntry) => unknown | undefined,
	deadlineMs: number = KV_SNAPSHOT_DEADLINE_MS
): Promise<KvSnapshot> {
	const values: Record<string, unknown> = {};
	let markInitialized: () => void = () => undefined;
	const initialized = new Promise<'initialized'>((resolve) => {
		markInitialized = () => resolve('initialized');
	});
	let timer: ReturnType<typeof setTimeout> | undefined;
	const deadline = new Promise<'deadline'>((resolve) => {
		timer = setTimeout(() => resolve('deadline'), deadlineMs);
	});

	let watch: Awaited<ReturnType<KvWatchSource['watch']>> | undefined;
	try {
		const opened = await Promise.race([kv.watch({ initializedFn: markInitialized }), deadline]);
		if (opened === 'deadline') return { values, complete: false };
		watch = opened;
		void collect(watch, pick, values);
		return { values, complete: (await Promise.race([initialized, deadline])) === 'initialized' };
	} finally {
		clearTimeout(timer);
		watch?.stop();
	}
}

async function collect(
	watch: AsyncIterable<KvEntry>,
	pick: (entry: KvEntry) => unknown | undefined,
	into: Record<string, unknown>
): Promise<void> {
	try {
		for await (const entry of watch) {
			const value = pick(entry);
			if (value !== undefined) into[entry.key] = value;
		}
	} catch {
		// the watch ended or failed; the snapshot keeps what arrived
	}
}
