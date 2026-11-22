import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { listMCPServerReports } from '#lib/server/k8s/index.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async () => {
	try {
		const result = await scopeList('', () => listMCPServerReports());
		return Response.json(result);
	} catch (e) {
		return kubeFailure(e, 'Failed to list MCP server reports');
	}
};
