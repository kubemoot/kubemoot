import type { RequestHandler } from './$types';
import { listMCPServerReports } from '#lib/server/k8s/index.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async () => {
	try {
		const result = await scopeList('', () => listMCPServerReports());
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list MCP server reports';
		return Response.json({ error: message }, { status: 500 });
	}
};
