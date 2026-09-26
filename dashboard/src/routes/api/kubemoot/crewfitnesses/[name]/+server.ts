import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getCrewFitness } from '$lib/server/k8s';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';

	try {
		const result = await getCrewFitness(namespace, params.name);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get crew fitness';
		return json({ error: message }, { status: 500 });
	}
};
