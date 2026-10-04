import type { RequestHandler } from './$types';
import { listPromptModules } from '#lib/server/k8s/index.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await scopeList(namespace, listPromptModules);
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list prompt modules';
		return Response.json({ error: message }, { status: 500 });
	}
};
