import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getCrew } from '$lib/server/k8s';
import { guardNamespace } from '$lib/server/scope';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;

	try {
		const result = await getCrew(namespace, params.name);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get crew';
		return json({ error: message }, { status: 500 });
	}
};
