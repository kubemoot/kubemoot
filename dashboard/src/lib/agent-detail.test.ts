import { describe, expect, it } from 'vitest';
import type { Agent } from '$types/kubemoot.js';
import {
	LIVENESS_BADGE,
	heartbeatLiveness,
	mcpServerNames,
	mcpServerRefs,
	visibleTools
} from './agent-detail';

describe('heartbeatLiveness', () => {
	const healthy = { nats: true, ollama: true };

	it('is live for a recent heartbeat with both dependencies reachable', () => {
		expect(heartbeatLiveness(30, healthy)).toBe('live');
		expect(heartbeatLiveness(300, healthy)).toBe('live');
	});

	it('is degraded when Ollama or NATS is unreachable', () => {
		expect(heartbeatLiveness(30, { nats: false, ollama: true })).toBe('degraded');
		expect(heartbeatLiveness(30, { nats: true, ollama: false })).toBe('degraded');
	});

	it('is stale after five minutes whatever the dependencies', () => {
		expect(heartbeatLiveness(301, healthy)).toBe('stale');
		expect(heartbeatLiveness(301, { nats: false, ollama: false })).toBe('stale');
	});

	it('maps every state to a badge', () => {
		expect(LIVENESS_BADGE.live).toEqual({ status: 'success', label: 'Live' });
		expect(LIVENESS_BADGE.degraded).toEqual({ status: 'warning', label: 'Degraded' });
		expect(LIVENESS_BADGE.stale).toEqual({ status: 'error', label: 'Stale' });
	});
});

describe('visibleTools', () => {
	const tools = [{ name: 'pods_list' }, { name: 'pods_delete' }, { name: 'nodes_top' }];

	it('keeps only the allow list when there is one, even with a deny list', () => {
		expect(
			visibleTools(tools, { enabledTools: ['nodes_top'], disabledTools: ['nodes_top'] })
		).toEqual([{ name: 'nodes_top' }]);
	});

	it('drops the deny list', () => {
		expect(visibleTools(tools, { disabledTools: ['pods_delete'] }).map((t) => t.name)).toEqual([
			'pods_list',
			'nodes_top'
		]);
	});

	it('shows all tools without a server ref or lists', () => {
		expect(visibleTools(tools, undefined)).toBe(tools);
		expect(visibleTools(tools, {})).toBe(tools);
	});
});

describe('mcpServerRefs', () => {
	it('returns the spec refs as they are, with their tool lists', () => {
		const refs = [{ name: 'k8s', enabledTools: ['pods_list'] }];
		expect(mcpServerRefs({ spec: { mcpServers: refs } } as unknown as Agent)).toBe(refs);
	});

	it('builds name-only refs from status for gateway agents', () => {
		const gateway = { spec: {}, status: { mcpServerStatus: [{ name: 'gw', ready: true }] } };
		expect(mcpServerRefs(gateway as unknown as Agent)).toEqual([{ name: 'gw' }]);
		expect(mcpServerRefs(null)).toEqual([]);
	});
});

describe('mcpServerNames', () => {
	it('prefers spec.mcpServers', () => {
		const agent = {
			spec: { mcpServers: [{ name: 'k8s' }] },
			status: { mcpServerStatus: [{ name: 'other' }] }
		} as unknown as Agent;
		expect(mcpServerNames(agent)).toEqual(['k8s']);
	});

	it('falls back to status for gateway agents, then to none', () => {
		const gateway = { spec: {}, status: { mcpServerStatus: [{ name: 'gw' }] } } as unknown as Agent;
		expect(mcpServerNames(gateway)).toEqual(['gw']);
		expect(mcpServerNames({ spec: {} } as unknown as Agent)).toEqual([]);
		expect(mcpServerNames(null)).toEqual([]);
	});
});
