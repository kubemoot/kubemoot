import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listMCPQualityPolicies } from '$lib/server/k8s';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? 'kubemoot';

	try {
		const result = await listMCPQualityPolicies(namespace);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list MCP quality policies';
		return json({ error: message }, { status: 500 });
	}
};
