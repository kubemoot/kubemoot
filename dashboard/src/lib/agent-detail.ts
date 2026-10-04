// View logic for the Agent detail page.

import type { Agent } from '#lib/types/kubemoot.js';

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
