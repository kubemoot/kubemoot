import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listCrewFitnessSuites } from '$lib/server/k8s';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await listCrewFitnessSuites(namespace);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list crew fitness suites';
		return json({ error: message }, { status: 500 });
	}
};
