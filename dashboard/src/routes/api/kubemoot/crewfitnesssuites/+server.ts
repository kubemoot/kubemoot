import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { listCrewFitnessSuites } from '#lib/server/k8s/index.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await scopeList(namespace, listCrewFitnessSuites);
		return Response.json(result);
	} catch (e) {
		return kubeFailure(e, 'Failed to list crew fitness suites');
	}
};
