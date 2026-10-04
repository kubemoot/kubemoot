import type { RequestHandler } from './$types';
import { getModelProvider } from '#lib/server/k8s/index.js';
import { guardNamespace } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace, 'infra');
	if (denied) return denied;
	const { name } = params;

	try {
		const result = await getModelProvider(namespace, name);
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get model provider';
		return Response.json({ error: message }, { status: 500 });
	}
};
