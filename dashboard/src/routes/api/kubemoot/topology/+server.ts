import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listAgents } from '$lib/server/k8s/kubemoot-crds.js';
import { topologyNode } from '$lib/server/topology';
import type { TopologyEdge, TopologyResponse } from '$types/kubemoot.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const agentsResult = await listAgents(namespace);
		// Nodes come from the agents; their role is the inline a2a config.
		const nodes = (agentsResult.items || []).map(topologyNode);
		const edges: TopologyEdge[] = [];

		const response: TopologyResponse = { nodes, edges };
		return json(response);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to build topology';
		return json({ error: message }, { status: 500 });
	}
};
