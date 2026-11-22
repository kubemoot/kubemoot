import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { deleteAs, getCrew } from '#lib/server/k8s/index.js';
import { guardNamespace } from '#lib/server/scope.js';
import { jsonError } from '#lib/server/json-error.js';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;

	try {
		const result = await getCrew(namespace, params.name);
		return Response.json(result);
	} catch (e) {
		return kubeFailure(e, 'Failed to get crew');
	}
};

/**
 * DELETE /api/kubemoot/crews/{name}?namespace=: deletes the Crew as the signed-in person.
 * The operator's finalizer removes its agents, discussion state, memory and bindings.
 */
export const DELETE: RequestHandler = async ({ params, url }) => {
	// A delete never guesses a namespace.
	const namespace = url.searchParams.get('namespace');
	if (!namespace) return jsonError(400, 'namespace is required');
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	return deleteAs('crews', namespace, params.name);
};
