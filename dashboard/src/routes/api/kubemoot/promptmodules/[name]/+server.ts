import type { RequestHandler } from './$types';
import { getPromptModule } from '#lib/server/k8s/index.js';
import { guardNamespace } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	const { name } = params;
	if (!name) {
		return Response.json({ error: 'name is required' }, { status: 400 });
	}

	try {
		const result = await getPromptModule(namespace, name);
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get prompt module';
		return Response.json({ error: message }, { status: 404 });
	}
};
