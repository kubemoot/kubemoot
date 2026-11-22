import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { listCrewNamespaces } from '#lib/server/k8s/index.js';
import { scopeItems } from '#lib/server/scope.js';

// Returns the crews (kubemoot.ai/crew-labeled namespaces) for the top-bar
// "Crew:" selector. Non-crew namespaces are intentionally excluded - the
// selector scopes the dashboard by crew, and "All crews" (no selection) shows
// everything, including shared/control-plane resources in the kubemoot namespace.
export const GET: RequestHandler = async () => {
	try {
		const crews = await scopeItems(await listCrewNamespaces(), (c) => c.namespace);
		return Response.json({ crews });
	} catch (e) {
		return kubeFailure(e, 'Failed to list crews');
	}
};
