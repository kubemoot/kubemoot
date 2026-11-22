import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
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
		return kubeFailure(e, 'Failed to get Kubemoot config');
	}
};
