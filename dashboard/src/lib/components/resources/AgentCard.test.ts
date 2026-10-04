import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Agent, AgentHeartbeat } from '#lib/types/kubemoot.js';
import { HEARTBEAT_STALE_AFTER_SECONDS } from '#lib/agent-liveness.js';
import AgentCard from './AgentCard.svelte';

const agent = {
	metadata: { name: 'k8s-specialist', namespace: 'pilot' },
	spec: { description: 'Kubernetes specialist' },
	status: { ready: true }
} as unknown as Agent;

function heartbeatAged(seconds: number, healthy = true): AgentHeartbeat {
	return {
		agent: 'k8s-specialist',
		timestamp: new Date(Date.now() - seconds * 1000).toISOString(),
		nats: healthy,
		ollama: healthy,
		model: 'qwen3:8b',
		lastInference: new Date(Date.now() - 5000).toISOString()
	};
}

function livenessDot(container: HTMLElement) {
	return container.querySelector('.liveness-dot');
}

describe('AgentCard link', () => {
	it('links to the agent detail route in its namespace', () => {
		const { container } = render(AgentCard, { props: { agent } });
		expect(container.querySelector('a.card')?.getAttribute('href')).toBe(
			'/agents/k8s-specialist?namespace=pilot'
		);
	});
});

describe('AgentCard liveness', () => {
	it('shows a recent healthy heartbeat as live, with its ages in the tooltip', () => {
		const { container } = render(AgentCard, { props: { agent, heartbeat: heartbeatAged(30) } });
		const dot = livenessDot(container);
		expect(dot?.classList.contains('live')).toBe(true);
		expect(dot?.getAttribute('title')).toContain('Heartbeat: 30s ago');
		expect(dot?.getAttribute('title')).toContain('Last inference: 5s ago');
	});

	it('shows an unreachable dependency as degraded', () => {
		const { container } = render(AgentCard, {
			props: { agent, heartbeat: heartbeatAged(30, false) }
		});
		expect(livenessDot(container)?.classList.contains('degraded')).toBe(true);
	});

	it('marks a heartbeat stale at the same threshold as the agent detail page', () => {
		const { container } = render(AgentCard, {
			props: { agent, heartbeat: heartbeatAged(HEARTBEAT_STALE_AFTER_SECONDS + 10) }
		});
		expect(livenessDot(container)?.classList.contains('stale')).toBe(true);
	});

	it('shows no liveness dot without a heartbeat', () => {
		const { container } = render(AgentCard, { props: { agent } });
		expect(livenessDot(container)).toBeNull();
	});
});
