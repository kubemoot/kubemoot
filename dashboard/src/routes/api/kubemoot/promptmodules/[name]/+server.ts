import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getPromptModule } from '$lib/server/k8s';
import { guardNamespace } from '$lib/server/scope';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	const { name } = params;
	if (!name) {
		return json({ error: 'name is required' }, { status: 400 });
	}

	try {
		const result = await getPromptModule(namespace, name);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get prompt module';
		return json({ error: message }, { status: 404 });
	}
};
