import type { RequestHandler } from './$types';
import { kubeFailure, kubeStatus, readAgentReplicas } from '#lib/server/k8s/index.js';
import { isToken } from '#lib/crewScope.js';
import { guardNamespace } from '#lib/server/scope.js';
import { jsonError } from '#lib/server/json-error.js';

/**
 * GET /api/kubemoot/agents/{name}/replicas?namespace=: how many instances of the agent run, from
 * its Deployment, as the signed-in person. A scale-to-zero agent that is idle is 0 of 0, not 0 of 1.
 */
export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	if (!isToken(params.name)) return Response.json({ error: 'name is required' }, { status: 400 });
	try {
		return Response.json(await readAgentReplicas(namespace, params.name));
	} catch (e) {
		if (kubeStatus(e) === 404) return jsonError(404, 'This agent has no deployment.');
		return kubeFailure(e, 'Failed to read the agent deployment');
	}
};
