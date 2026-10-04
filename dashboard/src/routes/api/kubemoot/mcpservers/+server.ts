import type { RequestHandler } from './$types';
import { listMCPServers } from '#lib/server/k8s/index.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? 'kubemoot';

	try {
		const result = await scopeList(namespace, listMCPServers);
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list MCP servers';
		return Response.json({ error: message }, { status: 500 });
	}
};
