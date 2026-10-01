// View logic for the Agent detail page.

import type { Agent, AgentHeartbeat } from '$types/kubemoot.js';

export type HeartbeatLiveness = 'live' | 'degraded' | 'stale';

/** How the liveness of a heartbeat shows as a badge. */
export const LIVENESS_BADGE: Record<
	HeartbeatLiveness,
	{ status: 'success' | 'warning' | 'error'; label: string }
> = {
	live: { status: 'success', label: 'Live' },
	degraded: { status: 'warning', label: 'Degraded' },
	stale: { status: 'error', label: 'Stale' }
};

/** Stale after 5 minutes, degraded when Ollama or NATS is unreachable, live otherwise. */
export function heartbeatLiveness(
	ageSeconds: number,
	heartbeat: Pick<AgentHeartbeat, 'nats' | 'ollama'>
): HeartbeatLiveness {
	if (ageSeconds > 300) return 'stale';
	return heartbeat.ollama && heartbeat.nats ? 'live' : 'degraded';
}

/** The tools an agent sees on an MCP server: its allow list, else all but its deny list. */
export function visibleTools<T extends { name: string }>(
	allTools: T[],
	ref: { enabledTools?: string[]; disabledTools?: string[] } | undefined
): T[] {
	if (ref?.enabledTools) {
		const enabled = new Set(ref.enabledTools);
		return allTools.filter((t) => enabled.has(t.name));
	}
	if (ref?.disabledTools) {
		const disabled = new Set(ref.disabledTools);
		return allTools.filter((t) => !disabled.has(t.name));
	}
	return allTools;
}

/** The MCP servers an agent uses: spec.mcpServers, else status.mcpServerStatus (gateway agents). */
export function mcpServerRefs(agent: Agent | null): { name: string }[] {
	return (
		agent?.spec.mcpServers || agent?.status?.mcpServerStatus?.map((s) => ({ name: s.name })) || []
	);
}

/** The names of the MCP servers an agent uses (see mcpServerRefs). */
export function mcpServerNames(agent: Agent | null): string[] {
	return mcpServerRefs(agent).map((m) => m.name);
}
