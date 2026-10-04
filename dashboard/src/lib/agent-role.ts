import type { Agent } from '#lib/types/kubemoot.js';

export type AgentRole =
	'coordinator' | 'tooler' | 'analyst' | 'researcher' | 'specialist' | 'system';

// Roles a crew sets in the kubemoot.ai/role label; "specialist" is kept only for
// legacy crews that still emit it.
const LABELLED_ROLES = new Set<string>([
	'coordinator',
	'tooler',
	'analyst',
	'researcher',
	'specialist'
]);

/** Onboarding, RTFM, and observer agents serve the system rather than a crew role. */
function isSystemAgent(annotations: Record<string, string>): boolean {
	return (
		annotations['kubemoot.ai/onboarding-mode'] === 'true' ||
		annotations['kubemoot.ai/rtfm-mode'] === 'true' ||
		annotations['kubemoot.ai/discuss-role'] === 'observer'
	);
}

/**
 * The agent's role from its kubemoot.ai/role label. System agents and agents without
 * the label are 'system'; an unrecognised label value is shown as a tooler.
 */
export function agentRole(agent: Agent): AgentRole {
	const role = agent.metadata.labels?.['kubemoot.ai/role'];
	if (role && LABELLED_ROLES.has(role)) return role as AgentRole;
	if (!role || isSystemAgent(agent.metadata.annotations || {})) return 'system';
	return 'tooler';
}
