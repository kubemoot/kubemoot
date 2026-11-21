// Reads for the NATS status page and the Overview tile: stream and account info,
// subject-filtered stream info, and bounded object listings. Nothing here writes data or
// changes configuration. A listing opens a short-lived ephemeral consumer, as the
// history read does, and stops it when done.
import type { JetStreamManager, NatsConnection, StreamInfo } from 'nats';
import { getNatsConnection, sc } from './nats-client.js';
import { isScoped } from './mode.js';
import { kvBucketAllowed, scopedNamespaces, streamAllowed } from './scope.js';
import { historyReadSummary } from './history-timing.js';
import {
	CREW_ENTRY_CAP,
	kvSubjectFilter,
	usageFromObjects,
	usageFromSubjects,
	type CrewStoreUsage,
	type ObjectEntry,
	type SubjectPage
} from '#lib/nats/crew-usage.js';
import { crewKeyFor, crewKeyedStores, jetstreamName, namespaceKeyFor, storeForStream, STORES, type StoreDefinition } from '#lib/nats/stores.js';
import type { AccountLimit, NatsServerInfo, NatsStatus, StoreStatus } from '#lib/nats/status.js';

/** How long the server waits for NATS before the page shows it as not reachable. */
const CONNECT_TIMEOUT_MS = 4000;
/** Most objects scanned in one object store before a crew's numbers become a lower bound. */
export const OBJECT_SCAN_CAP = 20_000;
const NS_PER_SECOND = 1_000_000_000;

function withTimeout<T>(work: Promise<T>, ms: number, what: string): Promise<T> {
	let timer: ReturnType<typeof setTimeout>;
	const timeout = new Promise<never>((_, reject) => {
		timer = setTimeout(() => reject(new Error(`${what} timed out after ${ms} ms`)), ms);
	});
	return Promise.race([work, timeout]).finally(() => clearTimeout(timer));
}

/** The limit of an account figure: -1 and 0 both mean none is set. */
function accountLimit(used: number, limit: number | undefined): AccountLimit {
	return { used, limit: limit !== undefined && limit > 0 ? limit : null };
}

function positive(n: number | undefined): number | null {
	return n !== undefined && n > 0 ? n : null;
}

/** The stream's discard policy; null when the server reports none. */
function discardOf(policy: string | undefined): 'old' | 'new' | null {
	return policy === 'old' || policy === 'new' ? policy : null;
}

function limitsOf(si: StreamInfo, kind: StoreStatus['kind']): StoreStatus['limits'] {
	const maxAge = positive(si.config.max_age);
	return {
		maxAgeSeconds: maxAge === null ? null : Math.round(maxAge / NS_PER_SECOND),
		maxBytes: positive(si.config.max_bytes),
		maxMessages: positive(si.config.max_msgs),
		history: kind === 'kv' ? positive(si.config.max_msgs_per_subject) : null,
		discard: discardOf(si.config.discard)
	};
}

function usageOf(si: StreamInfo): StoreStatus['usage'] {
	return {
		messages: si.state.messages,
		bytes: si.state.bytes,
		firstSeq: si.state.first_seq,
		lastSeq: si.state.last_seq,
		consumers: si.state.consumer_count,
		keys: si.state.num_subjects ?? null,
		firstTs: si.state.first_ts ?? null
	};
}

/** One stream's info as a store row. */
export function storeStatusOf(si: StreamInfo): StoreStatus {
	const definition = storeForStream(si.config.name) ?? null;
	const kind = definition?.kind ?? kindOfName(si.config.name);
	const name = definition?.name ?? si.config.name.replace(/^(KV|OBJ)_/, '');
	return { definition, name, kind, limits: limitsOf(si, kind), usage: usageOf(si) };
}

function kindOfName(streamName: string): 'stream' | 'kv' | 'object' {
	if (streamName.startsWith('KV_')) return 'kv';
	return streamName.startsWith('OBJ_') ? 'object' : 'stream';
}

/** Whether this dashboard may show a store: scoped mode serves the discussion stream and the heartbeat bucket only. */
export function storeVisible(store: Pick<StoreStatus, 'name' | 'kind'>): boolean {
	if (!isScoped()) return true;
	return store.kind === 'kv' ? kvBucketAllowed(store.name) : streamAllowed(store.name);
}

async function listStores(jsm: JetStreamManager): Promise<StoreStatus[]> {
	const stores: StoreStatus[] = [];
	for await (const si of jsm.streams.list()) {
		const store = storeStatusOf(si);
		if (storeVisible(store)) stores.push(store);
	}
	return stores.sort((a, b) => a.name.localeCompare(b.name));
}

/** Server version and name from the connection; uptime and connections from the monitoring port when it answers. */
async function serverInfo(nc: NatsConnection): Promise<NatsServerInfo> {
	const info = nc.info;
	const monitor = await monitoringVarz();
	return {
		version: info?.version ?? 'unknown',
		name: info?.server_name ?? '',
		uptime: monitor?.uptime ?? null,
		connections: monitor?.connections ?? null
	};
}

/** The monitoring endpoint is optional: NATS_MONITOR_URL, else port 8222 of the server's host. */
export function monitorUrl(natsUrl: string | undefined, override: string | undefined): string | null {
	if (override) return override;
	if (!natsUrl) return null;
	try {
		const url = new URL(natsUrl.split(',')[0]);
		return `http://${url.hostname}:8222/varz`;
	} catch {
		return null;
	}
}

async function monitoringVarz(): Promise<{ uptime: string; connections: number } | null> {
	const url = monitorUrl(process.env.NATS_URL, process.env.NATS_MONITOR_URL);
	if (!url) return null;
	try {
		const res = await fetch(url, { signal: AbortSignal.timeout(1500) });
		if (!res.ok) return null;
		const varz = (await res.json()) as { uptime?: string; connections?: number };
		return typeof varz.uptime === 'string' && typeof varz.connections === 'number'
			? { uptime: varz.uptime, connections: varz.connections }
			: null;
	} catch {
		return null;
	}
}

/** The store with its usage replaced by the share of the given namespaces; sequences and consumers are cluster-wide, so they are dropped. */
export function withNamespaceShare(store: StoreStatus, shares: CrewStoreUsage[]): StoreStatus {
	const sum = (pick: (u: CrewStoreUsage) => number) => shares.reduce((n, u) => n + pick(u), 0);
	return {
		...store,
		share: 'namespaces',
		usage: { messages: sum((u) => u.messages), bytes: sum((u) => u.bytes), firstSeq: 0, lastSeq: 0, consumers: 0, keys: sum((u) => u.keys) }
	};
}

/** On a scoped dashboard, each served store counts only the allowed namespaces' subjects. */
async function scopeStores(nc: NatsConnection, stores: StoreStatus[]): Promise<StoreStatus[]> {
	const namespaces = await scopedNamespaces();
	if (namespaces === null) return stores;
	return Promise.all(
		stores.map(async (store) => {
			const template = store.definition?.namespaceKey;
			if (!store.definition || !template) return { ...store, share: 'namespaces' as const, usage: { ...store.usage, messages: 0, bytes: 0, firstSeq: 0, lastSeq: 0, consumers: 0, keys: 0 } };
			const shares: CrewStoreUsage[] = [];
			for (const ns of namespaces) {
				const key = namespaceKeyFor(template, ns);
				const filter = store.kind === 'kv' ? kvSubjectFilter(store.name, key) : key;
				const page = await subjectPage(nc, jetstreamName(store.definition), filter);
				if (page) shares.push(usageFromSubjects(store.name, page, store.usage.messages, store.usage.bytes));
			}
			return withNamespaceShare(store, shares);
		})
	);
}

function disconnected(error: unknown): NatsStatus {
	return {
		connected: false,
		error: error instanceof Error ? error.message : 'NATS is not reachable',
		checkedAt: new Date().toISOString(),
		stores: [],
		absent: []
	};
}

/** The definitions of stores the server does not hold (some are created only with a chart value). */
export function absentStores(stores: StoreStatus[]): string[] {
	const present = new Set(stores.map((s) => s.name));
	return STORES.filter((d) => !present.has(d.name) && storeVisible(d)).map((d) => d.name);
}

/** Server, JetStream account and every store, or a disconnected status when NATS cannot be read. */
export async function readNatsStatus(): Promise<NatsStatus> {
	try {
		const nc = await withTimeout(getNatsConnection(), CONNECT_TIMEOUT_MS, 'NATS connection');
		const jsm = await nc.jetstreamManager();
		const [account, listed, server] = await Promise.all([jsm.getAccountInfo(), listStores(jsm), serverInfo(nc)]);
		const stores = await scopeStores(nc, listed);
		return {
			connected: true,
			scoped: isScoped(),
			checkedAt: new Date().toISOString(),
			server,
			account: {
				storage: accountLimit(account.storage, account.limits.max_storage),
				memory: accountLimit(account.memory, account.limits.max_memory),
				streams: accountLimit(account.streams, account.limits.max_streams),
				consumers: accountLimit(account.consumers, account.limits.max_consumers)
			},
			stores,
			absent: absentStores(stores),
			historyReads: historyReadSummary() ?? undefined
		};
	} catch (e) {
		return disconnected(e);
	}
}

// ------------------------------------------------------------------ per crew

export interface CrewRef {
	namespace: string;
	crew: string;
}

export interface CrewUsage extends CrewRef {
	stores: CrewStoreUsage[];
}

/** One page of a stream's subjects matching a filter, never following NATS's own paging. */
async function subjectPage(nc: NatsConnection, stream: string, filter: string): Promise<SubjectPage | null> {
	const reply = await nc.request(`$JS.API.STREAM.INFO.${stream}`, sc.encode(JSON.stringify({ subjects_filter: filter })), {
		timeout: 5000
	});
	const body = reply.json<{ error?: unknown; total?: number; state?: { subjects?: Record<string, number> } }>();
	if (body.error) return null;
	const subjects = body.state?.subjects ?? {};
	return { subjects, total: body.total ?? Object.keys(subjects).length };
}

async function streamUsage(
	nc: NatsConnection,
	store: StoreDefinition,
	crew: CrewRef,
	totals: StoreStatus
): Promise<CrewStoreUsage | null> {
	const key = crewKeyFor(store.crewKey ?? '', crew.namespace, crew.crew);
	const filter = store.kind === 'kv' ? kvSubjectFilter(store.name, key) : key;
	const page = await subjectPage(nc, jetstreamName(store), filter);
	return page && usageFromSubjects(store.name, page, totals.usage.messages, totals.usage.bytes);
}

/** Object infos of one store, bounded: stops at the scan cap and says so. */
export async function listObjectsBounded(nc: NatsConnection, bucket: string, cap: number): Promise<{ entries: ObjectEntry[]; capped: boolean }> {
	// views.os creates a store that is missing, so check it exists first: this module only reads.
	await (await nc.jetstreamManager()).streams.info(`OBJ_${bucket}`);
	const os = await nc.jetstream().views.os(bucket);
	const watch = await os.watch({ ignoreDeletes: true, includeHistory: true });
	const entries: ObjectEntry[] = [];
	let capped = false;
	for await (const info of watch) {
		if (info === null) break;
		if (entries.length >= cap) {
			capped = true;
			break;
		}
		entries.push({ name: info.name, size: info.size });
	}
	watch.stop();
	return { entries, capped };
}

/** The objects of one crew among a store's listing: names start with `<namespace>/<crew>/`. */
export function objectsOfCrew(entries: ObjectEntry[], prefix: string): ObjectEntry[] {
	return entries.filter((e) => e.name.startsWith(prefix)).slice(0, CREW_ENTRY_CAP);
}

/** Runs `tasks` with at most `limit` in flight. */
export async function inBatches<T>(tasks: (() => Promise<T>)[], limit: number): Promise<T[]> {
	const results: T[] = new Array(tasks.length);
	let next = 0;
	async function worker() {
		while (next < tasks.length) {
			const i = next++;
			results[i] = await tasks[i]();
		}
	}
	await Promise.all(Array.from({ length: Math.min(limit, tasks.length) }, worker));
	return results;
}

async function objectListings(nc: NatsConnection, stores: StoreDefinition[]) {
	const listings = new Map<string, { entries: ObjectEntry[]; capped: boolean }>();
	for (const store of stores.filter((s) => s.kind === 'object')) {
		try {
			listings.set(store.name, await listObjectsBounded(nc, store.name, OBJECT_SCAN_CAP));
		} catch {
			// the store is not there yet: no crew has data in it
		}
	}
	return listings;
}

/** A crew's share of every store keyed by crew; a store that cannot be read is left out. */
export async function readCrewUsage(crews: CrewRef[]): Promise<CrewUsage[]> {
	const nc = await withTimeout(getNatsConnection(), CONNECT_TIMEOUT_MS, 'NATS connection');
	// Whole-store totals, never a scoped share: a crew's bytes are its share of the whole store.
	const totals = new Map((await listStores(await nc.jetstreamManager())).map((s) => [s.name, s]));
	const keyed = crewKeyedStores().filter((s) => totals.has(s.name));
	const listings = await objectListings(nc, keyed);
	const tasks = crews.map((crew) => async (): Promise<CrewUsage> => {
		const usages: CrewStoreUsage[] = [];
		for (const store of keyed) {
			const usage = await usageOfCrewStore(nc, store, crew, totals, listings);
			if (usage) usages.push(usage);
		}
		return { ...crew, stores: usages };
	});
	return inBatches(tasks, 4);
}

async function usageOfCrewStore(
	nc: NatsConnection,
	store: StoreDefinition,
	crew: CrewRef,
	totals: Map<string, StoreStatus>,
	listings: Map<string, { entries: ObjectEntry[]; capped: boolean }>
): Promise<CrewStoreUsage | null> {
	try {
		if (store.kind === 'object') {
			const listing = listings.get(store.name);
			if (!listing) return null;
			const prefix = crewKeyFor(store.crewKey ?? '', crew.namespace, crew.crew);
			return usageFromObjects(store.name, objectsOfCrew(listing.entries, prefix), listing.capped);
		}
		return await streamUsage(nc, store, crew, totals.get(store.name) as StoreStatus);
	} catch {
		return null;
	}
}
