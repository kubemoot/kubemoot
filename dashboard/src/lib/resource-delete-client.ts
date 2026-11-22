// Deleting a crew or a fitness run from the dashboard, as the signed-in person. The browser calls
// the dashboard server, which deletes the resource with the person's own token; the cluster's
// answer, or its refusal, comes back here. Deleting the resource is all that happens: the
// operator's finalizer removes what hangs off it. Nothing in this file sees the token.

import { resolve } from '$app/paths';
import { refusalOf } from '#lib/ask-client.js';

/** The cluster or the dashboard refused: the status and the plain words to show. */
export class DeleteRefused extends Error {
	constructor(
		readonly status: number,
		message: string
	) {
		super(message);
		this.name = 'DeleteRefused';
	}
}

export type DeleteKind = 'crew' | 'suite';

function pathFor(kind: DeleteKind, namespace: string, name: string): string {
	return kind === 'crew'
		? `${resolve('/api/kubemoot/crews/[name]', { name })}?namespace=${encodeURIComponent(namespace)}`
		: resolve('/api/kubemoot/crewfitnesssuites/[namespace]/[name]', { namespace, name });
}

/** Deletes the crew or fitness run. Throws DeleteRefused with the words to show (403 when the person's RBAC refuses, 404 when it is gone). */
export async function deleteResource(fetchFn: typeof fetch, kind: DeleteKind, namespace: string, name: string): Promise<void> {
	let res: Response;
	try {
		res = await fetchFn(pathFor(kind, namespace, name), { method: 'DELETE' });
	} catch {
		throw new DeleteRefused(0, 'The dashboard server could not be reached. Check the connection and try again.');
	}
	if (res.ok) return;
	const refusal = await refusalOf(res);
	throw new DeleteRefused(refusal.status, refusal.message);
}
