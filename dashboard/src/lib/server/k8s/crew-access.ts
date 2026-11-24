// What a signed-in person may read of the crews, asked of the API server with their own token.
//
// A NATS-backed view reads conversations with the dashboard's own connection, which the bus cannot
// tie to a person. So that the person's RBAC still decides, each namespace that holds Crews is
// checked with a SelfSubjectAccessReview for `get crews` in that namespace, never a cluster-wide
// list of the person's: a person with rights in one namespace sees that namespace's conversations
// and no other.

import { getAuthorizationApi, getCustomObjectsApi, getServiceAccountCustomObjectsApi } from './client.js';
import { GROUP, VERSION } from './kubemoot-crds.js';

/** How many namespaces are reviewed at once. */
export const REVIEW_CONCURRENCY = 8;

type CrewList = { items?: { metadata?: { namespace?: string } }[] };

const listCrews = { group: GROUP, version: VERSION, plural: 'crews' };

function namespacesOf(list: CrewList): string[] {
	const names = new Set<string>();
	for (const item of list.items ?? []) {
		if (item.metadata?.namespace) names.add(item.metadata.namespace);
	}
	return [...names];
}

/**
 * The namespaces that hold at least one Crew, which is where a conversation can be. They are listed
 * with the dashboard's ServiceAccount (the chart grants it `list` on crews unless
 * `rbac.crewNamespaceDiscovery` is off); when that is refused, with the signed-in person's own token,
 * which then shows only what a cluster-wide reader may see. It says where to look, not what a person
 * may see: every namespace is reviewed with the person's token afterwards.
 */
export async function crewNamespaces(): Promise<string[]> {
	try {
		return namespacesOf((await getServiceAccountCustomObjectsApi().listClusterCustomObject(listCrews)) as CrewList);
	} catch (err) {
		console.warn(`The dashboard's ServiceAccount cannot list Crews (${errorClass(err)}); looking with the caller's own token`);
		return namespacesOf((await getCustomObjectsApi().listClusterCustomObject(listCrews)) as CrewList);
	}
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
			body: { spec: { resourceAttributes: { namespace, verb: 'get', group: GROUP, resource: 'crews' } } }
		});
		return review.status?.allowed === true;
	} catch {
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
