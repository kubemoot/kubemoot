import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listCrewNamespaces } from '$lib/server/k8s';

// Returns the crews (kubemoot.ai/crew-labeled namespaces) for the top-bar
// "Crew:" selector. Non-crew namespaces are intentionally excluded - the
// selector scopes the dashboard by crew, and "All crews" (no selection) shows
// everything, including shared/control-plane resources in the kubemoot namespace.
export const GET: RequestHandler = async () => {
	try {
		const crews = await listCrewNamespaces();
		return json({ crews });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list crews';
		return json({ error: message }, { status: 500 });
	}
};
