import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listPromptModules } from '$lib/server/k8s';
import { scopeList } from '$lib/server/scope';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await scopeList(namespace, listPromptModules);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list prompt modules';
		return json({ error: message }, { status: 500 });
	}
};
