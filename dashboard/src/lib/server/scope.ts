// The one place that decides which namespaces this dashboard instance may show.
//
// With KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR unset every helper here passes its
// input through untouched. With a selector set, the allowed namespaces are resolved
// against the live cluster (a label-selector list, cached for a short interval) and
// every route filters, refuses, or drops events outside that set. The selector is
// the single source of truth; no namespace name is hard-coded.
//
// Fail closed: when the namespace list cannot be read and nothing is cached, the
// helpers throw (routes answer 503) instead of showing everything.

import { getCoreApi } from './k8s/client.js';
import { infraNamespace, isReadOnly, isScoped, namespaceSelector } from './mode.js';
import type { KubemootList } from '#lib/types/kubemoot.js';
import { parseChatSubject, parseDiscussSubject } from '#lib/crewScope.js';

/**
 * namespaced: only namespaces matching the selector.
 * infra: those plus the operator namespace, for the shared infrastructure kinds
 * (model providers, models, embedding models, MCP catalogs and quality policies).
 */
export type ScopeKind = 'namespaced' | 'infra';

/** How long a resolved namespace set is trusted before the next request refreshes it. */
export const SCOPE_TTL_MS = 10_000;

/** How long a failed refresh may keep serving the last good set before the scope fails closed. */
export const SCOPE_MAX_STALE_MS = 10 * SCOPE_TTL_MS;

interface Resolved {
	selector: string;
	names: Set<string>;
	at: number;
}

let resolved: Resolved | null = null;
let inflight: Promise<Resolved> | null = null;

async function listMatching(selector: string): Promise<Set<string>> {
	const list = await getCoreApi().listNamespace({ labelSelector: selector });
	const names = new Set<string>();
	for (const ns of list.items) {
		if (ns.metadata?.name) names.add(ns.metadata.name);
	}
	return names;
}

function refresh(selector: string): Promise<Resolved> {
	inflight ??= listMatching(selector)
		.then((names) => (resolved = { selector, names, at: Date.now() }))
		.finally(() => {
			inflight = null;
		});
	return inflight;
}

/** The resolved set for this selector, or null when there is none. */
function known(selector: string): Resolved | null {
	return resolved?.selector === selector ? resolved : null;
}

function isFresh(r: Resolved | null): boolean {
	return r !== null && Date.now() - r.at < SCOPE_TTL_MS;
}

/** Forget the resolved set; tests use it, and nothing else needs to. */
export function resetScopeCache(): void {
	resolved = null;
	inflight = null;
}

/**
 * The namespaces matching the selector, or null when this instance is not scoped.
 * A failed refresh falls back to the last good set; with none, it throws.
 */
export async function scopedNamespaces(): Promise<Set<string> | null> {
	if (!isScoped()) return null;
	const selector = namespaceSelector();
	const current = known(selector);
	if (current && isFresh(current)) return current.names;
	try {
		return (await refresh(selector)).names;
	} catch (err) {
		if (current && Date.now() - current.at < SCOPE_MAX_STALE_MS) return current.names;
		throw err;
	}
}

function allows(names: Set<string>, namespace: string, kind: ScopeKind): boolean {
	return names.has(namespace) || (kind === 'infra' && namespace === infraNamespace());
}

/** True when `namespace` may be shown. Always true when not scoped. */
export async function namespaceAllowed(namespace: string, kind: ScopeKind = 'namespaced'): Promise<boolean> {
	const names = await scopedNamespaces();
	return names === null || allows(names, namespace, kind);
}

/**
 * A synchronous check for streams that test every event: it reads the last resolved
 * set and starts a refresh in the background when it is stale. Call
 * scopedNamespaces() once when the stream opens so the set exists. With no set it
 * refuses everything.
 */
export function namespaceAllowedNow(namespace: string, kind: ScopeKind = 'namespaced'): boolean {
	if (!isScoped()) return true;
	const selector = namespaceSelector();
	const current = known(selector);
	if (!isFresh(current)) refresh(selector).catch(() => undefined);
	return current !== null && Date.now() - current.at < SCOPE_MAX_STALE_MS && allows(current.names, namespace, kind);
}

function notFound(): Response {
	return Response.json({ error: 'not found' }, { status: 404 });
}

function unavailable(): Response {
	return Response.json({ error: 'namespace scope unavailable' }, { status: 503 });
}

/**
 * A route's gate for one namespace: null to continue, or the response to return.
 * Out of scope is 404, the same answer as a namespace that does not exist.
 */
export async function guardNamespace(
	namespace: string | null | undefined,
	kind: ScopeKind = 'namespaced'
): Promise<Response | null> {
	try {
		return (await namespaceAllowed(namespace ?? '', kind)) ? null : notFound();
	} catch {
		return unavailable();
	}
}

/** A route's gate for the object-store and NATS keys that start with `<namespace>/`. */
export async function guardKeyNamespace(
	parsed: { namespace: string } | null,
	kind: ScopeKind = 'namespaced'
): Promise<Response | null> {
	return parsed ? guardNamespace(parsed.namespace, kind) : notFound();
}

/** The CRD plurals that are shared infrastructure and stay readable in the operator namespace. */
export const INFRA_PLURALS: ReadonlySet<string> = new Set([
	'modelproviders',
	'models',
	'embeddingmodels',
	'mcpcatalogs',
	'mcpqualitypolicies'
]);

export function scopeKindFor(plural: string): ScopeKind {
	return INFRA_PLURALS.has(plural) ? 'infra' : 'namespaced';
}

/**
 * A watch route's gate: a named namespace is checked like any other; the all-namespaces
 * watch ('') only needs the scope resolved, because its events are filtered one by one.
 */
export async function guardNamespaceOrAll(namespace: string, kind: ScopeKind = 'namespaced'): Promise<Response | null> {
	if (namespace !== '') return guardNamespace(namespace, kind);
	try {
		await scopedNamespaces();
		return null;
	} catch {
		return unavailable();
	}
}

/** For crdWatchResponse: accepts a watched object only when its namespace is allowed. */
export function objectAccepter(kind: ScopeKind = 'namespaced'): (obj: unknown) => boolean {
	return (obj) => {
		const ns = (obj as { metadata?: { namespace?: string } } | null)?.metadata?.namespace ?? '';
		return namespaceAllowedNow(ns, kind);
	};
}

/**
 * Lists a CRD through `list`, limited to the allowed namespaces. A namespace outside
 * the scope yields an empty list; the cluster-wide list ('') is filtered item by item.
 */
export async function scopeList<T extends { metadata?: { namespace?: string } }>(
	namespace: string,
	list: (namespace: string) => Promise<KubemootList<T>>,
	kind: ScopeKind = 'namespaced'
): Promise<KubemootList<T>> {
	const names = await scopedNamespaces();
	if (names === null) return list(namespace);
	if (namespace !== '' && !allows(names, namespace, kind)) {
		return { apiVersion: '', kind: '', metadata: {}, items: [] };
	}
	const result = await list(namespace);
	return {
		...result,
		items: result.items.filter((item) => allows(names, item.metadata?.namespace ?? '', kind))
	};
}

/** Keeps the items whose namespace (read by `namespaceOf`) is allowed. */
export async function scopeItems<T>(
	items: T[],
	namespaceOf: (item: T) => string,
	kind: ScopeKind = 'namespaced'
): Promise<T[]> {
	const names = await scopedNamespaces();
	return names === null ? items : items.filter((item) => allows(names, namespaceOf(item), kind));
}

// ------------------------------------------------------------------- NATS

/** The only JetStream stream the dashboard reads; other names are refused when restricted. */
export const DISCUSSION_STREAM = 'KUBEMOOT_DISCUSS';

/** The only key-value bucket /api/nats/kv serves when restricted (agent heartbeats). */
export const KV_BUCKETS_ALLOWED: ReadonlySet<string> = new Set(['kubemoot_agent_state']);

/** Whether the dashboard may read this JetStream stream. Scoped mode serves only the discussion stream. */
export function streamAllowed(name: string): boolean {
	return !isScoped() || name === DISCUSSION_STREAM;
}

/**
 * Whether /api/nats/kv may open this bucket. Opening a bucket that does not exist
 * creates it, so read-only and scoped modes serve a fixed list instead of any name.
 */
export function kvBucketAllowed(name: string): boolean {
	return !(isScoped() || isReadOnly()) || KV_BUCKETS_ALLOWED.has(name);
}

/** The namespace token of a `kubemoot.discuss.<ns>...` or `kubemoot.chat.<ns>...` subject. */
export function subjectNamespace(subject: string): string | null {
	const tokens = subject.split('.');
	const head = `${tokens[0]}.${tokens[1]}`;
	if (head !== 'kubemoot.discuss' && head !== 'kubemoot.chat') return null;
	return tokens[2] ?? null;
}

const WILDCARDS = new Set(['*', '>']);

/**
 * Whether a subject FILTER (a subscribe, stream or purge pattern) may be opened.
 * Only discussion and chat subjects carry a namespace. A literal namespace must be
 * allowed; a wildcard namespace passes and its messages are filtered one by one.
 */
export async function subjectFilterAllowed(subject: string): Promise<boolean> {
	return subjectAllowed(subject, true);
}

/**
 * Whether a write may target this subject: like subjectFilterAllowed, but the
 * namespace must be a literal allowed name, because a wildcard would reach
 * namespaces outside the scope.
 */
export async function subjectTargetAllowed(subject: string): Promise<boolean> {
	return subjectAllowed(subject, false);
}

async function subjectAllowed(subject: string, wildcardNamespace: boolean): Promise<boolean> {
	let names: Set<string> | null;
	try {
		names = await scopedNamespaces();
	} catch {
		return false;
	}
	if (names === null) return true;
	const ns = subjectNamespace(subject);
	if (ns === null) return false;
	return names.has(ns) || (wildcardNamespace && WILDCARDS.has(ns));
}

/** Whether one concrete message subject belongs to an allowed namespace. */
export function messageSubjectAllowed(subject: string): boolean {
	if (!isScoped()) return true;
	const parsed = parseDiscussSubject(subject) ?? parseChatSubject(subject);
	return parsed !== null && namespaceAllowedNow(parsed.namespace);
}

/** Whether a KV key of the form `<namespace>.<name>...` belongs to an allowed namespace. */
export function keyNamespaceAllowed(key: string): boolean {
	if (!isScoped()) return true;
	const ns = key.split('.')[0];
	return ns !== '' && namespaceAllowedNow(ns);
}

/** A stream read's gate (history, stream): the stream and the subject filter must both be served. */
export async function guardStreamRead(stream: string, subject: string): Promise<Response | null> {
	if (!streamAllowed(stream)) return forbidden('stream');
	return (await subjectFilterAllowed(subject)) ? null : forbidden('subject');
}

/** The key-value read's gate: the bucket must be served, and the scope resolved to filter its keys. */
export async function guardKvRead(bucket: string): Promise<Response | null> {
	if (!kvBucketAllowed(bucket)) return forbidden('bucket');
	return guardNamespaceOrAll('');
}

/** The response that refuses a subject, stream or bucket outside what scoped mode serves. */
export function forbidden(what: string): Response {
	return Response.json({ error: `${what} is not available on this dashboard` }, { status: 403 });
}
