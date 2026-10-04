import type { Agent } from '#lib/types/kubemoot.js';

export interface ChannelInfo {
	name: string;
	color: string;
	agentCount: number;
}

// Consistent colors for known channels, auto-assign for dynamic ones
const CHANNEL_COLORS: Record<string, string> = {
	kubernetes: '#3b82f6',
	observability: '#f59e0b',
	proxmox: '#8b5cf6',
	general: '#6b7280'
};
const KNOWN_ORDER = Object.keys(CHANNEL_COLORS);

const DYNAMIC_COLORS = ['#ec4899', '#14b8a6', '#f97316', '#06b6d4', '#84cc16', '#e11d48'];

/** Subscriber count per channel from the agents' a2a config; 'general' is always present. */
export function countChannelSubscribers(agents: Agent[]): Map<string, number> {
	const counts = new Map<string, number>();
	for (const agent of agents) {
		for (const ch of agent.spec?.a2a?.subscribeChannels || []) {
			counts.set(ch, (counts.get(ch) || 0) + 1);
		}
	}
	if (!counts.has('general')) counts.set('general', 0);
	return counts;
}

/** Known channels first in their fixed order, then the rest alphabetically. */
export function compareChannels(a: ChannelInfo, b: ChannelInfo): number {
	const ai = KNOWN_ORDER.indexOf(a.name);
	const bi = KNOWN_ORDER.indexOf(b.name);
	if (ai !== -1 && bi !== -1) return ai - bi;
	if (ai !== -1) return -1;
	if (bi !== -1) return 1;
	return a.name.localeCompare(b.name);
}

/**
 * The channel list with colors, sorted for display. Known channels keep their color;
 * the others take the dynamic palette in first-seen order.
 */
export function buildChannels(agents: Agent[]): ChannelInfo[] {
	let dynamicIdx = 0;
	const channels: ChannelInfo[] = [];
	for (const [name, agentCount] of countChannelSubscribers(agents)) {
		const color = CHANNEL_COLORS[name] || DYNAMIC_COLORS[dynamicIdx++ % DYNAMIC_COLORS.length];
		channels.push({ name, color, agentCount });
	}
	return channels.sort(compareChannels);
}
