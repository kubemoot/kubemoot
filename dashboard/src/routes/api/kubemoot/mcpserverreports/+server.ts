import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listMCPServerReports } from '$lib/server/k8s';
import { scopeList } from '$lib/server/scope';

export const GET: RequestHandler = async () => {
	try {
		const result = await scopeList('', () => listMCPServerReports());
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list MCP server reports';
		return json({ error: message }, { status: 500 });
	}
};
