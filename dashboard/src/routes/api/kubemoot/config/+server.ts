import type { RequestHandler } from './$types';
import { listKubemootConfigs, getKubemootConfig } from '#lib/server/k8s/index.js';

export const GET: RequestHandler = async ({ url }) => {
	const name = url.searchParams.get('name');

	try {
		if (name) {
			const result = await getKubemootConfig(name);
			return Response.json(result);
		} else {
			const result = await listKubemootConfigs();
			return Response.json(result);
		}
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get Kubemoot config';
		return Response.json({ error: message }, { status: 500 });
	}
};
