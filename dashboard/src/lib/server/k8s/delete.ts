import { getCustomObjectsApi } from './client.js';
import { callerToken } from '../auth/identity.js';
import { jsonError } from '../json-error.js';
import { kubeStatus } from './errors.js';

// The dashboard's only direct Kubernetes writes: deleting a Crew or a CrewFitnessSuite as the
// signed-in person. The call carries the person's own token, so their RBAC decides; with no
// signed-in person nothing is sent, never the dashboard's own ServiceAccount. Deleting the
// resource is all the dashboard does; the operator's finalizer removes what hangs off it.

const GROUP = 'kubemoot.ai';
const VERSION = 'v1alpha1';

/** What a person may delete here (the resource's plural), with the noun used when it is refused or gone. */
export const DELETABLE = {
	crews: 'crew',
	crewfitnesssuites: 'fitness run'
} as const;

export type DeletableKind = keyof typeof DELETABLE;

/** Deletes the resource as the signed-in person; throws what the API server answered. */
export async function deleteResource(kind: DeletableKind, namespace: string, name: string): Promise<void> {
	await getCustomObjectsApi().deleteNamespacedCustomObject({ group: GROUP, version: VERSION, namespace, plural: kind, name });
}

/** The plain-words answer for a delete the API server refused: not allowed, not signed in, or already gone. */
export function deleteFailure(err: unknown, kind: DeletableKind): Response {
	const noun = DELETABLE[kind];
	const status = kubeStatus(err);
	if (status === 403) {
		return jsonError(403, `You do not have permission to delete this ${noun}. Your Kubernetes account is not allowed to; ask the cluster administrator for access.`, { denied: true });
	}
	if (status === 401) {
		return jsonError(401, 'The cluster no longer accepts your sign-in. Sign in again.', { denied: true });
	}
	if (status === 404) return jsonError(404, `This ${noun} no longer exists.`);
	return jsonError(500, err instanceof Error ? err.message : `Failed to delete the ${noun}.`);
}

/** The route handler's whole job: delete as the caller and answer in plain words. */
export async function deleteAs(kind: DeletableKind, namespace: string, name: string): Promise<Response> {
	if (!callerToken()) return jsonError(401, 'Sign in to delete.', { signIn: true });
	try {
		await deleteResource(kind, namespace, name);
		return Response.json({ deleted: true, kind, namespace, name });
	} catch (err) {
		return deleteFailure(err, kind);
	}
}
