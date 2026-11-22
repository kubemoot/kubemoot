import type { RequestHandler } from './$types';
import { getCrewFitnessSuite } from '#lib/server/k8s/index.js';
import { guardNamespace } from '#lib/server/scope.js';

/** GET /api/kubemoot/crewfitnesssuites/{namespace}/{name}: the one suite as Kubernetes returns it. */
export const GET: RequestHandler = async ({ params }) => {
	const { namespace, name } = params;
	const denied = await guardNamespace(namespace);
	if (denied) return denied;

	try {
		return Response.json(await getCrewFitnessSuite(namespace, name));
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get fitness suite';
		return Response.json({ error: message }, { status: 500 });
	}
};
