// What a signed-in person may read of the crews, asked of the API server with their own token.
//
// A NATS-backed view reads conversations with the dashboard's own connection, which the bus cannot
// tie to a person. So that the person's RBAC still decides, each namespace that holds Crews is
// checked with a SelfSubjectAccessReview for `get crews` in that namespace, never a cluster-wide
// list of the person's: a person with rights in one namespace sees that namespace's conversations
// and no other.

import { getAuthorizationApi, getServiceAccountCustomObjectsApi } from './client.js';
import { readKvSnapshot } from '../kv-snapshot.js';
import { crewNamespaceDiscovery } from '../mode.js';
import { getNatsConnection } from '../nats-client.js';
import { GROUP, VERSION } from './kubemoot-crds.js';

/** The bucket of agent heartbeats, keyed `<namespace>.<agent>`. */
const HEARTBEAT_BUCKET = 'kubemoot_agent_state';

/** The namespace a key of the form `<namespace>.<name>...` belongs to, or '' when it names none. */
export function keyNamespace(key: string): string {
	return key.split('.')[0];
}

const WARN_EVERY_MS = 60_000;
const lastWarned = new Map<string, number>();

/** Warns at most once a minute per message, so a failing review does not flood the log. */
export function warnLimited(message: string): void {
	const now = Date.now();
	if (now - (lastWarned.get(message) ?? -WARN_EVERY_MS) < WARN_EVERY_MS) return;
	lastWarned.set(message, now);
	console.warn(message);
}

/** How many namespaces are reviewed at once. */
export const REVIEW_CONCURRENCY = 8;

type CrewList = { items?: { metadata?: { namespace?: string } }[] };

const CREWS_REF = { group: GROUP, version: VERSION, plural: 'crews' };

function namespacesOf(list: CrewList): string[] {
	const names = new Set<string>();
	for (const item of list.items ?? []) {
		if (item.metadata?.namespace) names.add(item.metadata.namespace);
	}
	return [...names];
}

/**
 * The namespace of each agent key in the heartbeat bucket the dashboard already reads
 * (`<namespace>.<agent>`): the namespaces that have run agents.
 */
export async function natsNamespaces(): Promise<string[]> {
	const kv = await (await getNatsConnection()).jetstream().views.kv(HEARTBEAT_BUCKET, { bindOnly: true });
	const { values, complete } = await readKvSnapshot(kv, (entry) => (entry.operation === 'PUT' ? keyNamespace(entry.key) : undefined));
	if (!complete) warnLimited('The agent heartbeat bucket was not fully read; some namespaces may be missing');
	return [...new Set(Object.values(values).filter((ns): ns is string => typeof ns === 'string' && ns !== ''))];
}

/** The namespaces that hold a Crew, listed with the dashboard's ServiceAccount; only when the chart granted it. */
async function listedNamespaces(): Promise<string[]> {
	if (!crewNamespaceDiscovery()) return [];
	return namespacesOf((await getServiceAccountCustomObjectsApi().listClusterCustomObject(CREWS_REF)) as CrewList);
}

/**
 * The namespaces to review for a person: those with agents on the bus, and, when the chart lets
 * the ServiceAccount list Crews, those that hold a Crew. It says where to look, not what a person
 * may see: every namespace is reviewed with the person's token afterwards. A source that fails
 * adds nothing, so with none the answer is empty and the views show nothing.
 */
export async function crewNamespaces(): Promise<string[]> {
	const found = await Promise.allSettled([natsNamespaces(), listedNamespaces()]);
	const names = new Set<string>();
	for (const source of found) {
		if (source.status === 'fulfilled') source.value.forEach((ns) => names.add(ns));
		else warnLimited(`A source of crew namespaces failed: ${errorClass(source.reason)}`);
	}
	return [...names];
}

/** What went wrong, by class and message only: an error never carries the token. */
export function errorClass(err: unknown): string {
	return err instanceof Error ? `${err.name}: ${err.message}` : 'unknown error';
}

/**
 * Whether the signed-in person may `get` Crews in the namespace, by a SelfSubjectAccessReview with
 * their own token. A review that cannot be made, or an answer that is not a plain yes, is no.
 */
export async function mayGetCrews(namespace: string): Promise<boolean> {
	try {
		const review = await getAuthorizationApi().createSelfSubjectAccessReview({
			body: {
				spec: {
					resourceAttributes: {
						namespace,
						verb: 'get',
						group: GROUP,
						resource: 'crews'
					}
				}
			}
		});
		return review.status?.allowed === true;
	} catch (err) {
		warnLimited(`A review of access to Crews could not be made: ${errorClass(err)}`);
		return false;
	}
}

/** The namespaces of `candidates` where the signed-in person may get Crews, a few reviews at a time. */
export async function readableNamespaces(candidates: string[]): Promise<Set<string>> {
	const allowed = new Set<string>();
	const queue = [...candidates];
	const worker = async (): Promise<void> => {
		for (let ns = queue.shift(); ns !== undefined; ns = queue.shift()) {
			if (await mayGetCrews(ns)) allowed.add(ns);
		}
	};
	await Promise.all(Array.from({ length: Math.min(REVIEW_CONCURRENCY, queue.length) }, worker));
	return allowed;
}
