import type { RequestHandler } from './$types';
import { listAgents } from '#lib/server/k8s/kubemoot-crds.js';
import { topologyNode } from '#lib/server/topology.js';
import type { TopologyEdge, TopologyResponse } from '#lib/types/kubemoot.js';
import { scopeList } from '#lib/server/scope.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const agentsResult = await scopeList(namespace, listAgents);
		// Nodes come from the agents; their role is the inline a2a config.
		const nodes = (agentsResult.items || []).map(topologyNode);
		const edges: TopologyEdge[] = [];

		const response: TopologyResponse = { nodes, edges };
		return Response.json(response);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to build topology';
		return Response.json({ error: message }, { status: 500 });
	}
};
