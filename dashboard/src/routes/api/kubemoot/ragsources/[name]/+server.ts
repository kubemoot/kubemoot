import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getRAGSource } from '$lib/server/k8s';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const { name } = params;

	try {
		const result = await getRAGSource(namespace, name);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get RAG source';
		return json({ error: message }, { status: 500 });
	}
};
