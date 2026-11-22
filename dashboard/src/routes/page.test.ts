import { render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Page from './+page.svelte';

const ready = { ready: true, phase: 'Ready' };
const node = (name: string, readyStatus: 'True' | 'False') => ({
	metadata: { name },
	status: { conditions: [{ type: 'Ready', status: readyStatus, reason: readyStatus === 'True' ? 'KubeletReady' : 'KubeletNotReady' }], allocatable: { cpu: '8', memory: '16Gi', pods: '110' } }
});

/** What each API answers; a path not listed answers 503 so the tile says Unavailable. */
function serve(routes: Record<string, object>) {
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: string) => {
			const path = input.split('?')[0];
			const key = Object.keys(routes).find((k) => path.endsWith(k));
			return key ? new Response(JSON.stringify(routes[key]), { status: 200 }) : new Response('{}', { status: 503 });
		})
	);
}

const healthyCluster = {
	'/componentstatuses': [{ name: 'operator', healthy: true, message: 'ready' }],
	'/api/nodes': { nodes: [node('n1', 'True'), node('n2', 'True')] },
	'/api/node-usage': { metrics: { available: true, data: { n1: { cpu: '4', memory: '8Gi' }, n2: { cpu: '4', memory: '8Gi' } } }, pods: { available: true, data: {} } },
	'/crews': { items: [{ metadata: { name: 'pilot', namespace: 'crew-pilot' }, spec: {}, status: ready }] },
	'/modelservers': { items: [{ metadata: { name: 'rig0', namespace: 'kubemoot' }, spec: {}, status: { ...ready, models: [{ name: 'a' }, { name: 'b' }], capacity: { loadedModels: [{ name: 'a' }] } } }] },
	'/models': { items: [] },
	'/embeddingmodels': { items: [] },
	'/mcpservers': { items: [] },
	'/mcpgateways': { items: [] },
	'/ragsources': { items: [] },
	'/agents': { items: [] },
	'/crewfitnesssuites': { items: [] },
	'/api/nats/status': { connected: true, checkedAt: '2026-11-22T18:00:00Z', server: { version: '2.12.0' }, stores: [], absent: [] }
};

beforeEach(() => {
	vi.stubGlobal('EventSource', class { addEventListener() {} close() {} });
});
afterEach(() => vi.unstubAllGlobals());

describe('the Overview page', () => {
	it('shows tile states and numbers, no Control plane heading, and no Needs attention list when all is well', async () => {
		serve(healthyCluster);
		const { container } = render(Page);
		await waitFor(() => expect(screen.getByTestId('tile-nodes').textContent).toContain('2/2'));

		expect(container.textContent).not.toMatch(/control plane/i);
		expect(screen.queryByTestId('needs-attention')).toBeNull();
		// The Models tile counts the catalogs the servers report, as the Models page lists them.
		expect(screen.getByTestId('tile-models').textContent).toContain('2/2');
		expect(screen.getByTestId('tile-nodes').textContent).toContain('Healthy');
		expect(screen.getByTestId('tile-nodes').querySelectorAll('[role="meter"]')).toHaveLength(2);
		expect(screen.getByTestId('tile-nats').textContent).toContain('Healthy');
	});

	it('lists what needs attention at the top, each item linking to its page, and marks the tile', async () => {
		serve({ ...healthyCluster, '/api/nodes': { nodes: [node('n1', 'True'), node('gpu-3', 'False')] } });
		render(Page);
		const list = await screen.findByTestId('needs-attention');
		expect(list.textContent).toContain('Node gpu-3');
		expect(list.textContent).toContain('Not Ready (KubeletNotReady)');
		expect(list.querySelector('a')?.getAttribute('href')).toBe('/nodes/gpu-3');
		expect(screen.getByTestId('tile-nodes').textContent).toContain('Down');
	});

	it('says Unavailable on a tile whose source failed, never a zero', async () => {
		const rest: Record<string, object> = { ...healthyCluster };
		delete rest['/crews'];
		serve(rest);
		render(Page);
		await waitFor(() => expect(screen.getByTestId('tile-crews').textContent).toContain('Unavailable'));
		expect(screen.getByTestId('tile-crews').textContent).not.toMatch(/\b0\b/);
		expect(screen.getByTestId('tile-nodes').textContent).toContain('2/2');
	});

	it('has the NATS tile as one cell like the others', () => {
		const css = [...document.querySelectorAll('style')].map((s) => s.textContent).join('');
		expect(css).not.toContain('span 2');
	});
});
