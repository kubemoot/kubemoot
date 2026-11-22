import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { listNodes } from '#lib/server/k8s/index.js';

export const GET: RequestHandler = async () => {
	try {
		const nodes = await listNodes();
		return Response.json({ nodes });
	} catch (e) {
		return kubeFailure(e, 'Failed to list nodes');
	}
};
