import type { Agent, TopologyNode } from '#lib/types/kubemoot.js';

/** The agent's topology role from its inline a2a config; anything else is a peer. */
export function topologyRole(agent: Agent): TopologyNode['role'] {
	const role = agent.spec.a2a?.role;
	return role === 'coordinator' || role === 'specialist' ? role : 'peer';
}

/** Ready when the agent is ready, error on an Error or Failed phase, pending otherwise. */
export function topologyStatus(agent: Agent): TopologyNode['status'] {
	if (agent.status?.ready) return 'ready';
	const phase = agent.status?.phase;
	return phase === 'Error' || phase === 'Failed' ? 'error' : 'pending';
}

/** Total MCP tools across the agent's MCP servers. */
export function agentToolCount(agent: Agent): number {
	return (agent.status?.mcpServerStatus || []).reduce((sum, s) => sum + (s.toolCount || 0), 0);
}

/** The topology graph node for one agent. */
export function topologyNode(agent: Agent): TopologyNode {
	return {
		id: agent.metadata.name,
		namespace: agent.metadata.namespace || '',
		role: topologyRole(agent),
		status: topologyStatus(agent),
		endpoint: agent.status?.endpoint || '',
		toolCount: agentToolCount(agent),
		peerCount: 0,
		description: agent.spec.description || '',
		labels: agent.metadata.labels || {}
	};
}
