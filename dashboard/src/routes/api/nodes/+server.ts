import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listNodes } from '$lib/server/k8s';

export const GET: RequestHandler = async () => {
	try {
		const nodes = await listNodes();
		return json({ nodes });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list nodes';
		return json({ error: message }, { status: 500 });
	}
};
