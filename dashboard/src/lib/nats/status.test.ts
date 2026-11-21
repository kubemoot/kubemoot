import { describe, expect, it } from 'vitest';
import {
	allHistoryNotices,
	allWarnings,
	formatAge,
	formatBytes,
	formatDuration,
	formatLimit,
	fraction,
	historyFill,
	historyNotice,
	historyNoticeText,
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

describe('history of a stream that drops its oldest messages at a cap', () => {
	const now = new Date('2026-11-21T12:00:00Z');
	const SEVEN_DAYS = 604_800;
	const hoursAgo = (h: number) => new Date(now.getTime() - h * 3600_000).toISOString();
	const full = (over: StoreOver = {}) =>
		store({
			name: 'KUBEMOOT_OPERATOR',
			limits: { discard: 'old', maxAgeSeconds: SEVEN_DAYS, maxMessages: 10_000, maxBytes: null, ...over.limits },
			usage: { messages: 10_000, bytes: 2_000_000, firstTs: hoursAgo(6), ...over.usage }
		});

	it('raises no warning at the cap', () => {
		expect(storeWarnings(full())).toEqual([]);
		expect(tileOf(status({ stores: [full()] }))?.state).toBe('connected');
	});

	it('says how much history it keeps against the intended age when the cap is full', () => {
		const notice = historyNotice(full(), now);
		expect(notice).toEqual({ store: 'KUBEMOOT_OPERATOR', cap: 'messages', keptSeconds: 21_600, intendedSeconds: SEVEN_DAYS });
		expect(historyNoticeText(notice!)).toBe(
			'KUBEMOOT_OPERATOR keeps 6 hours of the intended 7 days, because its message cap is full'
		);
	});

	it('names the byte cap when that is the full one', () => {
		const s = full({ limits: { maxBytes: 2_000_000, maxMessages: 1_000_000 } });
		expect(historyNotice(s, now)?.cap).toBe('bytes');
	});

	it('says nothing when the oldest message already spans the max age', () => {
		expect(historyNotice(full({ usage: { firstTs: hoursAgo(24 * 8) } }), now)).toBeNull();
	});

	it('says nothing while the cap has room, even a young stream near it', () => {
		expect(historyNotice(full({ usage: { messages: 500 } }), now)).toBeNull();
		expect(historyNotice(full({ usage: { messages: 9_000 } }), now)).toBeNull();
	});

	it('says nothing without a max age, an oldest time, or messages', () => {
		expect(historyNotice(full({ limits: { maxAgeSeconds: null } }), now)).toBeNull();
		expect(historyNotice(full({ usage: { firstTs: null } }), now)).toBeNull();
		expect(historyNotice(full({ usage: { firstTs: '0001-01-01T00:00:00Z' } }), now)).toBeNull();
		expect(historyNotice(full({ usage: { messages: 0 } }), now)).toBeNull();
	});

	it('does not apply to a stream that refuses writes at its limit', () => {
		expect(historyNotice(full({ limits: { discard: 'new' } }), now)).toBeNull();
	});

	it('lists the notices from a status time and skips an unreadable one', () => {
		const stores = [full()];
		expect(allHistoryNotices(stores, now.toISOString())).toHaveLength(1);
		expect(allHistoryNotices(stores, 'now')).toEqual([]);
		expect(allHistoryNotices(stores, undefined)).toEqual([]);
	});
});

describe('a stream that refuses writes at its limit', () => {
	it('keeps a real warning near the limit', () => {
		const s = store({ limits: { discard: 'new' }, usage: { messages: 96 } });
		expect(storeWarnings(s)).toEqual([{ store: 's', kind: 'messages', fraction: 0.96, level: 'critical' }]);
		expect(tileOf(status({ stores: [s] }))?.state).toBe('near-limit');
	});

	it('is quiet while it has room', () => {
		expect(storeWarnings(store({ limits: { discard: 'new' } }))).toEqual([]);
	});
});

describe('formatDuration', () => {
	it('uses minutes, hours and days, singular and plural', () => {
		expect(formatDuration(30)).toBe('1 minute');
		expect(formatDuration(1800)).toBe('30 minutes');
		expect(formatDuration(3600)).toBe('1 hour');
		expect(formatDuration(21_600)).toBe('6 hours');
		expect(formatDuration(86_400)).toBe('24 hours');
		expect(formatDuration(604_800)).toBe('7 days');
		expect(formatDuration(2_592_000)).toBe('30 days');
	});
});
