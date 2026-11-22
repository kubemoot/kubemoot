import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { listCrews } from '#lib/server/k8s/index.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await scopeList(namespace, listCrews);
		return Response.json(result);
	} catch (e) {
		return kubeFailure(e, 'Failed to list crews');
	}
};
