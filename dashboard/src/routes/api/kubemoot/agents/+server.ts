import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listAgents } from '$lib/server/k8s';
import { scopeList } from '$lib/server/scope';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? 'kubemoot';

	try {
		const result = await scopeList(namespace, listAgents);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list agents';
		return json({ error: message }, { status: 500 });
	}
};
