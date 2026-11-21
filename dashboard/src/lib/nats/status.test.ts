import { describe, expect, it } from 'vitest';
import {
	allWarnings,
	formatAge,
	formatBytes,
	formatLimit,
	fraction,
	historyFill,
	storeWarnings,
	tileOf,
	type NatsStatus,
	type StoreStatus
} from './status';

interface StoreOver {
	name?: string;
	kind?: StoreStatus['kind'];
	usage?: Partial<StoreStatus['usage']>;
	limits?: Partial<StoreStatus['limits']>;
}

function store(over: StoreOver = {}): StoreStatus {
	return {
		definition: null,
		name: over.name ?? 's',
		kind: over.kind ?? 'stream',
		limits: { maxAgeSeconds: null, maxBytes: 1000, maxMessages: 100, history: null, ...over.limits },
		usage: { messages: 10, bytes: 100, firstSeq: 1, lastSeq: 10, consumers: 0, keys: null, ...over.usage }
	};
}

function status(over: Partial<NatsStatus> = {}): NatsStatus {
	return {
		connected: true,
		checkedAt: 'now',
		server: { version: '2.12.0', name: 'n', uptime: null, connections: null },
		account: {
			storage: { used: 100, limit: 1000 },
			memory: { used: 0, limit: null },
			streams: { used: 3, limit: null },
			consumers: { used: 0, limit: null }
		},
		stores: [store()],
		absent: [],
		...over
	};
}

describe('fraction', () => {
	it('is the share of a limit', () => expect(fraction(50, 200)).toBe(0.25));
	it.each([null, undefined, 0, -1])('is null for the limit %s', (limit) => expect(fraction(5, limit)).toBeNull());
});

describe('store warnings', () => {
	it('is quiet below 80%', () => expect(storeWarnings(store({ usage: { bytes: 790, messages: 79 } }))).toEqual([]));

	it('warns at 80% of bytes or of messages', () => {
		const w = storeWarnings(store({ name: 'a', usage: { bytes: 800, messages: 80 } }));
		expect(w.map((x) => [x.kind, x.level])).toEqual([
			['bytes', 'warning'],
			['messages', 'warning']
		]);
	});

	it('is critical at 95%', () => {
		expect(storeWarnings(store({ usage: { bytes: 960 } }))[0].level).toBe('critical');
	});

	it('does not warn about a limit that is not set', () => {
		expect(storeWarnings(store({ limits: { maxBytes: null, maxMessages: null }, usage: { bytes: 1e9, messages: 1e9 } }))).toEqual([]);
	});

	it('orders the worst first', () => {
		const all = allWarnings([store({ name: 'a', usage: { bytes: 850 } }), store({ name: 'b', usage: { bytes: 990 } })]);
		expect(all.map((w) => w.store)).toEqual(['b', 'a']);
	});
});

describe('KV history fill', () => {
	const kv = (messages: number, keys: number | null, history: number | null) =>
		store({ kind: 'kv', usage: { messages, keys }, limits: { history } });

	it('is revisions stored against keys times depth', () => expect(historyFill(kv(8, 2, 5))).toBe(0.8));
	it('is not meaningful for a depth of one', () => expect(historyFill(kv(5, 5, 1))).toBeNull());
	it('is null for a stream and for an empty bucket', () => {
		expect(historyFill(store())).toBeNull();
		expect(historyFill(kv(0, 0, 5))).toBeNull();
	});
	it('never raises a warning, since a busy key sits at its depth by design', () => {
		expect(storeWarnings(kv(10, 2, 5))).toEqual([]);
	});
});

describe('the overview tile', () => {
	it('is null until the first read lands', () => expect(tileOf(null)).toBeNull());

	it('is connected with the server version, storage and totals', () => {
		const tile = tileOf(status({ stores: [store({ usage: { messages: 5, bytes: 10 } }), store({ usage: { messages: 7, bytes: 20 } })] }));
		expect(tile?.state).toBe('connected');
		expect(tile?.version).toBe('2.12.0');
		expect(tile?.storage).toEqual({ used: 100, limit: 1000, fraction: 0.1 });
		expect(tile?.memory?.fraction).toBeNull();
		expect([tile?.messages, tile?.bytes, tile?.streams]).toEqual([12, 30, 2]);
		expect(tile?.warnings).toEqual([]);
	});

	it('is disconnected with the reason, and shows no warnings', () => {
		const tile = tileOf({ connected: false, error: 'refused', checkedAt: 'now', stores: [store({ usage: { bytes: 999 } })], absent: [] });
		expect(tile?.state).toBe('disconnected');
		expect(tile?.error).toBe('refused');
		expect(tile?.warnings).toEqual([]);
	});

	it('is near a limit when a store passes 80%', () => {
		const tile = tileOf(status({ stores: [store({ name: 'KUBEMOOT_DISCUSS', usage: { messages: 85 } })] }));
		expect(tile?.state).toBe('near-limit');
		expect(tile?.warnings[0]).toMatchObject({ store: 'KUBEMOOT_DISCUSS', kind: 'messages' });
	});

	it('is near a limit when the account storage passes 80%', () => {
		const tile = tileOf(status({ account: { ...status().account!, storage: { used: 900, limit: 1000 } } }));
		expect(tile?.state).toBe('near-limit');
	});
});

describe('formatting', () => {
	it.each([
		[0, '0 B'],
		[1023, '1023 B'],
		[1536, '1.5 KiB'],
		[268435456, '256 MiB'],
		[1073741824, '1.0 GiB']
	])('formats %d bytes as %s', (n, text) => expect(formatBytes(n)).toBe(text));

	it.each([
		[null, 'none'],
		[0, 'none'],
		[86400, '1d'],
		[24 * 3600 * 7, '1w'],
		[5 * 31_536_000, '5y'],
		[300, '5m'],
		[90, '90s']
	])('formats the age %s as %s', (s, text) => expect(formatAge(s)).toBe(text));

	it('says unlimited for a limit that is not set', () => {
		expect(formatLimit(null)).toBe('unlimited');
		expect(formatLimit(-1)).toBe('unlimited');
		expect(formatLimit(2048, formatBytes)).toBe('2.0 KiB');
	});
});

describe('a scoped dashboard\'s share', () => {
	const share = store({ name: 'KUBEMOOT_DISCUSS', usage: { bytes: 990, messages: 99 } });
	share.share = 'namespaces';

	it('raises no store warning, since a share says nothing of how full the store is', () => {
		expect(storeWarnings(share)).toEqual([]);
	});

	it('marks the tile scoped, and a cluster status not', () => {
		expect(tileOf(status({ scoped: true, stores: [share] }))).toMatchObject({ scoped: true, state: 'connected', messages: 99 });
		expect(tileOf(status())?.scoped).toBe(false);
	});
});
