import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listMCPServerReports } from '$lib/server/k8s';

export const GET: RequestHandler = async () => {
	try {
		const result = await listMCPServerReports();
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list MCP server reports';
		return json({ error: message }, { status: 500 });
	}
};
