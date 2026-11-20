import type { RequestHandler } from './$types';
import { getMCPServerReport } from '#lib/server/k8s/index.js';
import { guardNamespace } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	try {
		const result = await getMCPServerReport(namespace, params.name);
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get MCP server report';
		return Response.json({ error: message }, { status: 500 });
	}
};
