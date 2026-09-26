import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getMCPQualityPolicy } from '$lib/server/k8s';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const { name } = params;

	try {
		const result = await getMCPQualityPolicy(namespace, name);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get MCP quality policy';
		return json({ error: message }, { status: 500 });
	}
};
