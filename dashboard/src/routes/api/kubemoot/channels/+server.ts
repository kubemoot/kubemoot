import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listAgents } from '$lib/server/k8s/kubemoot-crds.js';
import type { Agent } from '$types/kubemoot.js';

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

const DYNAMIC_COLORS = ['#ec4899', '#14b8a6', '#f97316', '#06b6d4', '#84cc16', '#e11d48'];

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') ?? '';

	try {
		const result = await listAgents(namespace);
		const agents: Agent[] = result.items || [];

		// Collect all unique channels from agent a2a config
		const channelAgentCount = new Map<string, number>();
		for (const agent of agents) {
			const channels = agent.spec?.a2a?.subscribeChannels || [];
			for (const ch of channels) {
				channelAgentCount.set(ch, (channelAgentCount.get(ch) || 0) + 1);
			}
		}

		// Always include 'general' even if no agent subscribes to it
		if (!channelAgentCount.has('general')) {
			channelAgentCount.set('general', 0);
		}

		// Build channel info with colors
		let dynamicIdx = 0;
		const channels: ChannelInfo[] = [];
		for (const [name, agentCount] of channelAgentCount) {
			const color =
				CHANNEL_COLORS[name] || DYNAMIC_COLORS[dynamicIdx++ % DYNAMIC_COLORS.length];
			channels.push({ name, color, agentCount });
		}

		// Sort: known channels first (by predefined order), then dynamic alphabetically
		const knownOrder = Object.keys(CHANNEL_COLORS);
		channels.sort((a, b) => {
			const ai = knownOrder.indexOf(a.name);
			const bi = knownOrder.indexOf(b.name);
			if (ai !== -1 && bi !== -1) return ai - bi;
			if (ai !== -1) return -1;
			if (bi !== -1) return 1;
			return a.name.localeCompare(b.name);
		});

		return json({ channels });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list channels';
		return json({ error: message }, { status: 500 });
	}
};
