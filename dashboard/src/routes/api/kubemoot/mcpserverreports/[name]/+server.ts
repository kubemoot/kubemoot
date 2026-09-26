import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getMCPServerReport, patchMCPServerReport } from '$lib/server/k8s';

export const GET: RequestHandler = async ({ params, url }) => {
	try {
		const namespace = url.searchParams.get('namespace') || 'kubemoot';
		const result = await getMCPServerReport(namespace, params.name);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to get MCP server report';
		return json({ error: message }, { status: 500 });
	}
};

export const PATCH: RequestHandler = async ({ params, request, url }) => {
	try {
		const namespace = url.searchParams.get('namespace') || 'kubemoot';
		const body = await request.json();

		const patch = {
			spec: {
				adminVerdict: body.adminVerdict ?? '',
				adminNotes: body.adminNotes ?? '',
				adminAuthor: body.adminAuthor ?? ''
			}
		};

		const result = await patchMCPServerReport(namespace, params.name, patch);
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to update MCP server report';
		return json({ error: message }, { status: 500 });
	}
};
