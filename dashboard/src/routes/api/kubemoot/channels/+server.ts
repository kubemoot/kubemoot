import type { RequestHandler } from './$types';
import { listAgents } from '#lib/server/k8s/kubemoot-crds.js';
import { buildChannels } from '#lib/server/channels.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await scopeList(namespace, listAgents);
		return Response.json({ channels: buildChannels(result.items || []) });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list channels';
		return Response.json({ error: message }, { status: 500 });
	}
};
