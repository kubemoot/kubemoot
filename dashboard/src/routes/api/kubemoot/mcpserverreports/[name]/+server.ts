import type { RequestHandler } from './$types';
import { getMCPServerReport, patchMCPServerReport } from '#lib/server/k8s/index.js';
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

export const PATCH: RequestHandler = async ({ params, request, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	try {
		const body = await request.json();

		const patch = {
			spec: {
				adminVerdict: body.adminVerdict ?? '',
				adminNotes: body.adminNotes ?? '',
				adminAuthor: body.adminAuthor ?? ''
			}
		};

		const result = await patchMCPServerReport(namespace, params.name, patch);
		return Response.json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to update MCP server report';
		return Response.json({ error: message }, { status: 500 });
	}
};
