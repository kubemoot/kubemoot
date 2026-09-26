import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listAgents } from '$lib/server/k8s/kubemoot-crds.js';
import type { Agent, TopologyNode, TopologyEdge, TopologyResponse } from '$types/kubemoot.js';

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const agentsResult = await listAgents(namespace);
		const agents = agentsResult.items || [];

		const nodes: TopologyNode[] = [];
		const edges: TopologyEdge[] = [];

		// Build nodes from agents — role comes from inline a2a config
		for (const agent of agents) {
			const a2a = agent.spec.a2a;

			let role: TopologyNode['role'] = 'peer';
			if (a2a?.role === 'coordinator') role = 'coordinator';
			else if (a2a?.role === 'specialist') role = 'specialist';

			const toolCount = (agent.status?.mcpServerStatus || [])
				.reduce((sum, s) => sum + (s.toolCount || 0), 0);

			let status: TopologyNode['status'] = 'pending';
			if (agent.status?.ready) status = 'ready';
			else if (agent.status?.phase === 'Error' || agent.status?.phase === 'Failed') status = 'error';

			nodes.push({
				id: agent.metadata.name,
				namespace: agent.metadata.namespace || '',
				role,
				status,
				endpoint: agent.status?.endpoint || '',
				toolCount,
				peerCount: 0,
				description: agent.spec.description || '',
				labels: agent.metadata.labels || {}
			});
		}

		const response: TopologyResponse = { nodes, edges };
		return json(response);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to build topology';
		return json({ error: message }, { status: 500 });
	}
};
