// What the NATS tile and the NATS status page show, and the rules that decide it.
// The server reads NATS and fills these types; nothing here talks to NATS.
import type { StoreDefinition } from './stores.js';

export interface StoreLimits {
	maxAgeSeconds: number | null;
	maxBytes: number | null;
	maxMessages: number | null;
	/** KV only: revisions kept per key. */
	history: number | null;
	/** What the stream does at a limit: 'old' drops the oldest messages, 'new' refuses writes. */
	discard?: 'old' | 'new' | null;
}

export interface StoreUsage {
	messages: number;
	bytes: number;
	firstSeq: number;
	lastSeq: number;
	consumers: number;
	/** Distinct subjects (KV keys) the stream holds, when the server says. */
	keys: number | null;
	/** When the oldest message the stream holds was stored (ISO time); null when empty or unknown. */
	firstTs?: string | null;
}

export interface StoreStatus {
	definition: StoreDefinition | null;
	/**
	 * 'cluster': the usage is the whole store's. 'namespaces': the usage is only the share
	 * of the namespaces a scoped dashboard serves; sequences and consumers are not shown.
	 */
	share?: 'cluster' | 'namespaces';
	/** The bucket or stream name as a person knows it (no KV_ or OBJ_ prefix). */
	name: string;
	kind: 'stream' | 'kv' | 'object';
	limits: StoreLimits;
	usage: StoreUsage;
}

export interface AccountLimit {
	used: number;
	/** null when the account sets no limit. */
	limit: number | null;
}

export interface NatsServerInfo {
	version: string;
	name: string;
	uptime: string | null;
	connections: number | null;
}

export interface NatsStatus {
	connected: boolean;
	/** True on a namespace-scoped dashboard: store usage is that scope's share, not cluster-wide. */
	scoped?: boolean;
	error?: string;
	checkedAt: string;
	server?: NatsServerInfo;
	account?: { storage: AccountLimit; memory: AccountLimit; streams: AccountLimit; consumers: AccountLimit };
	stores: StoreStatus[];
	/** Definitions of stores the server does not hold. */
	absent: string[];
	historyReads?: HistoryReadSummary;
}

export interface HistoryReadSummary {
	count: number;
	lastMs: number;
	avgMs: number;
	maxMs: number;
	lastBytes: number;
	lastMessages: number;
}

/** A use at or above this share of a limit raises the warning band. */
export const WARN_FRACTION = 0.8;
/** At or above this share a cap counts as reached, so a discard-old stream is dropping messages. */
export const FULL_FRACTION = 0.99;
/** At or above this share, writes are about to be refused or the oldest data dropped. */
export const CRITICAL_FRACTION = 0.95;

export type LimitKind = 'bytes' | 'messages' | 'history';

export interface LimitWarning {
	store: string;
	kind: LimitKind;
	fraction: number;
	level: 'warning' | 'critical';
}

/** Share of a limit used; null when there is no limit (unset, zero or negative). */
export function fraction(used: number, limit: number | null | undefined): number | null {
	if (limit === null || limit === undefined || limit <= 0) return null;
	return used / limit;
}

function levelOf(f: number): 'warning' | 'critical' | null {
	if (f >= CRITICAL_FRACTION) return 'critical';
	return f >= WARN_FRACTION ? 'warning' : null;
}

/**
 * The limits of one store that are at or above the warning share: bytes and messages.
 * A store that drops its oldest messages at a limit is meant to sit at it, so it raises
 * no warning here; historyNotice says how much history it keeps instead.
 */
export function storeWarnings(store: StoreStatus): LimitWarning[] {
	// A namespace's share says nothing about how full the whole store is.
	if (store.share === 'namespaces') return [];
	if (store.limits.discard === 'old') return [];
	const checks: [LimitKind, number | null][] = [
		['bytes', fraction(store.usage.bytes, store.limits.maxBytes)],
		['messages', fraction(store.usage.messages, store.limits.maxMessages)]
	];
	const out: LimitWarning[] = [];
	for (const [kind, f] of checks) {
		const level = f === null ? null : levelOf(f);
		if (f !== null && level) out.push({ store: store.name, kind, fraction: f, level });
	}
	return out;
}

/**
 * KV history in use: stored revisions against keys times the history depth. A key that
 * is rewritten often sits at its depth by design, because the oldest revision rotates
 * out, so it is shown and never raised as a warning.
 */
export function historyFill(store: StoreStatus): number | null {
	const { history } = store.limits;
	if (store.kind !== 'kv' || !history || history <= 1 || !store.usage.keys) return null;
	return store.usage.messages / (store.usage.keys * history);
}

/** Every warning across the stores, worst first. */
export function allWarnings(stores: StoreStatus[]): LimitWarning[] {
	return stores
		.flatMap(storeWarnings)
		.sort((a, b) => b.fraction - a.fraction);
}

/** A store that drops its oldest messages at a size cap and so keeps less history than its max age. */
export interface HistoryNotice {
	store: string;
	/** The cap that is full. */
	cap: 'bytes' | 'messages';
	keptSeconds: number;
	intendedSeconds: number;
}

const MS_PER_SECOND = 1000;

/** The cap of a discard-old store that is reached, the fuller one first. */
function fullCap(store: StoreStatus): 'bytes' | 'messages' | null {
	const bytes = fraction(store.usage.bytes, store.limits.maxBytes);
	const messages = fraction(store.usage.messages, store.limits.maxMessages);
	const full = (f: number | null) => f !== null && f >= FULL_FRACTION;
	if (full(bytes) && (!full(messages) || bytes! >= messages!)) return 'bytes';
	return full(messages) ? 'messages' : null;
}

/** Seconds from the oldest message to now; null when the store is empty or the time is unreadable. */
function oldestAgeSeconds(store: StoreStatus, now: Date): number | null {
	const first = store.usage.firstTs ? Date.parse(store.usage.firstTs) : NaN;
	// An empty stream reports the zero time (year 1).
	if (Number.isNaN(first) || store.usage.messages <= 0 || first < MS_PER_SECOND) return null;
	return Math.max(0, Math.round((now.getTime() - first) / MS_PER_SECOND));
}

/**
 * For a stream that drops its oldest messages at a size cap: how much history it keeps
 * against the max age it is meant to keep. Null when the store is not in that state: the
 * cap is not reached, there is no max age, or the oldest message already spans the max age.
 */
export function historyNotice(store: StoreStatus, now: Date): HistoryNotice | null {
	const { discard, maxAgeSeconds } = store.limits;
	if (store.share === 'namespaces' || discard !== 'old' || !maxAgeSeconds) return null;
	const cap = fullCap(store);
	const kept = oldestAgeSeconds(store, now);
	if (cap === null || kept === null || kept >= maxAgeSeconds) return null;
	return { store: store.name, cap, keptSeconds: kept, intendedSeconds: maxAgeSeconds };
}

/** The history notices across the stores, the shortest history relative to its intent first. */
export function allHistoryNotices(stores: StoreStatus[], checkedAt: string | undefined): HistoryNotice[] {
	const now = new Date(checkedAt ?? '');
	if (Number.isNaN(now.getTime())) return [];
	return stores
		.map((s) => historyNotice(s, now))
		.filter((n): n is HistoryNotice => n !== null)
		.sort((a, b) => a.keptSeconds / a.intendedSeconds - b.keptSeconds / b.intendedSeconds);
}

function plural(n: number, unit: string): string {
	return `${n} ${unit}${n === 1 ? '' : 's'}`;
}

/** A span in seconds in words: 6 hours, 7 days. Two days or more is shown in days. */
export function formatDuration(seconds: number): string {
	if (seconds >= 2 * 86_400) return plural(Math.round(seconds / 86_400), 'day');
	if (seconds >= 3600) return plural(Math.round(seconds / 3600), 'hour');
	return plural(Math.max(1, Math.round(seconds / 60)), 'minute');
}

/** The notice as a sentence: "KUBEMOOT_OPERATOR keeps 6 hours of the intended 7 days". */
export function historyNoticeText(n: HistoryNotice): string {
	const cap = n.cap === 'bytes' ? 'size' : 'message';
	return `${n.store} keeps ${formatDuration(n.keptSeconds)} of the intended ${formatDuration(n.intendedSeconds)}, because its ${cap} cap is full`;
}

export type TileState = 'connected' | 'near-limit' | 'disconnected';

export interface NatsTile {
	state: TileState;
	/** True when the totals are a scoped dashboard's namespaces' share, not the whole cluster's. */
	scoped: boolean;
	version: string | null;
	storage: { used: number; limit: number | null; fraction: number | null } | null;
	memory: { used: number; limit: number | null; fraction: number | null } | null;
	messages: number;
	bytes: number;
	streams: number;
	warnings: LimitWarning[];
	error: string | null;
}

function usageOf(a: AccountLimit | undefined) {
	return a ? { used: a.used, limit: a.limit, fraction: fraction(a.used, a.limit) } : null;
}

/** The tile's state: not connected, near a limit (a store or the account), or connected. */
function tileState(connected: boolean, warnings: LimitWarning[], account: (ReturnType<typeof usageOf>)[]): TileState {
	if (!connected) return 'disconnected';
	const accountNear = account.some((u) => u?.fraction != null && u.fraction >= WARN_FRACTION);
	return warnings.length > 0 || accountNear ? 'near-limit' : 'connected';
}

/** The overview tile's content from a status; null while the first read has not landed. */
export function tileOf(status: NatsStatus | null): NatsTile | null {
	if (!status) return null;
	const warnings = status.connected ? allWarnings(status.stores) : [];
	const storage = usageOf(status.account?.storage);
	const memory = usageOf(status.account?.memory);
	return {
		state: tileState(status.connected, warnings, [storage, memory]),
		scoped: status.scoped === true,
		version: status.server?.version ?? null,
		storage,
		memory,
		messages: status.stores.reduce((n, s) => n + s.usage.messages, 0),
		bytes: status.stores.reduce((n, s) => n + s.usage.bytes, 0),
		streams: status.stores.length,
		warnings,
		error: status.error ?? null
	};
}

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

export function formatBytes(bytes: number): string {
	let value = bytes;
	let unit = 0;
	while (value >= 1024 && unit < UNITS.length - 1) {
		value /= 1024;
		unit++;
	}
	const text = unit === 0 || value >= 100 ? Math.round(value).toString() : value.toFixed(1);
	return `${text} ${UNITS[unit]}`;
}

/** "unlimited" for a limit that is unset. */
export function formatLimit(limit: number | null, format: (n: number) => string = String): string {
	return limit === null || limit <= 0 ? 'unlimited' : format(limit);
}

export function formatPercent(f: number | null): string {
	return f === null ? '' : `${Math.round(f * 100)}%`;
}

const AGE_STEPS: [number, string][] = [
	[31_536_000, 'y'],
	[604_800, 'w'],
	[86_400, 'd'],
	[3600, 'h'],
	[60, 'm'],
	[1, 's']
];

/** An age in seconds as one short number and unit (5y, 24h); "none" when there is no expiry. */
export function formatAge(seconds: number | null): string {
	if (!seconds || seconds <= 0) return 'none';
	for (const [size, unit] of AGE_STEPS) {
		if (seconds % size === 0) return `${seconds / size}${unit}`;
	}
	return `${seconds}s`;
}
