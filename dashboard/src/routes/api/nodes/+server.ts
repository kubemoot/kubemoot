import type { RequestHandler } from './$types';
import { listNodes } from '#lib/server/k8s/index.js';

export const GET: RequestHandler = async () => {
	try {
		const nodes = await listNodes();
		return Response.json({ nodes });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list nodes';
		return Response.json({ error: message }, { status: 500 });
	}
};
