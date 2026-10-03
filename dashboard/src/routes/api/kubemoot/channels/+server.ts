import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listAgents } from '$lib/server/k8s/kubemoot-crds.js';
import { buildChannels } from '$lib/server/channels';
import { scopeList } from '$lib/server/scope';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await scopeList(namespace, listAgents);
		return json({ channels: buildChannels(result.items || []) });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list channels';
		return json({ error: message }, { status: 500 });
	}
};
