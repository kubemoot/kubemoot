import type { RequestHandler } from './$types';
import { deleteCrew, getCrew } from '#lib/server/k8s/index.js';
import { guardNamespace } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;

	try {
		const result = await getCrew(namespace, params.name);
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get crew';
		return Response.json({ error: message }, { status: 500 });
	}
};

/**
 * DELETE /api/kubemoot/crews/{name}?namespace={namespace}
 *
 * Deletes the Crew object only. The operator removes its Agents and open
 * discussions through its finalizer and owner references.
 */
export const DELETE: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || '';
	if (!namespace) {
		return Response.json({ error: 'namespace is required' }, { status: 400 });
	}
	const denied = await guardNamespace(namespace);
	if (denied) return denied;

	try {
		await deleteCrew(namespace, params.name);
		return Response.json({ deleted: `${namespace}/${params.name}` });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to delete crew';
		return Response.json({ error: message }, { status: 500 });
	}
};
