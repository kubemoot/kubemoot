import { render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPageUrl } from '../../../../vitest-mocks/app-state.svelte.js';
import Page from './+page.svelte';

const node = {
	apiVersion: 'v1',
	kind: 'Node',
	metadata: { name: 'gpu-1', labels: { 'kubernetes.io/hostname': 'gpu-1' }, creationTimestamp: '2026-11-01T00:00:00Z' },
	spec: { taints: [{ key: 'nvidia.com/gpu', value: 'true', effect: 'NoSchedule' }] },
	status: {
		conditions: [
			{ type: 'Ready', status: 'True', reason: 'KubeletReady', lastTransitionTime: '2026-11-20T10:00:00Z' },
			{ type: 'MemoryPressure', status: 'True', reason: 'KubeletHasInsufficientMemory', lastTransitionTime: '2026-11-22T10:00:00Z' }
		],
		capacity: { cpu: '8', memory: '16Gi', pods: '110', 'nvidia.com/gpu': '1' },
		allocatable: { cpu: '8', memory: '16Gi', pods: '110' },
		addresses: [{ type: 'InternalIP', address: 'node-address-1' }],
		nodeInfo: { osImage: 'Talos', kernelVersion: '6.18' }
	},
	accelerator: { present: true, type: 'RTX 5090', count: 1, memory: '32GB' }
};

function answer(body: object, status = 200) {
	return vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }));
}

beforeEach(() => {
	setPageUrl('/nodes/gpu-1');
	page.params = { name: 'gpu-1' };
});
afterEach(() => vi.unstubAllGlobals());

describe('the node page', () => {
	it('shows conditions with reasons, capacity against use, the accelerator, the pods, taints and the facts', async () => {
		vi.stubGlobal(
			'fetch',
			answer({
				node,
				metrics: { available: true, data: { 'gpu-1': { cpu: '2', memory: '8Gi' } } },
				pods: {
					available: true,
					data: [
						{ name: 'vllm-0', namespace: 'kubemoot', phase: 'Running', restarts: 3, accelerators: 1 },
						{ name: 'stuck', namespace: 'apps', phase: 'Pending', restarts: 0, accelerators: 0 }
					]
				}
			})
		);
		const { container } = render(Page);
		await waitFor(() => expect(screen.getByTestId('conditions')).toBeTruthy());

		const conditions = within(screen.getByTestId('conditions'));
		expect(conditions.getByText('KubeletHasInsufficientMemory')).toBeTruthy();
		expect(screen.getByTestId('node-problems').textContent).toContain('MemoryPressure');

		const capacity = screen.getByTestId('capacity').textContent ?? '';
		expect(capacity).toContain('2 cores (25%)');
		expect(capacity).toContain('8 GiB (50%)');

		const pods = within(screen.getByTestId('pods'));
		expect(pods.getByText('vllm-0')).toBeTruthy();
		expect(pods.getByText('Pending')).toBeTruthy();
		expect(screen.getByText('Pods (2)')).toBeTruthy();

		expect(container.textContent).toContain('RTX 5090');
		expect(container.textContent).toContain('1 of 1');
		expect(container.textContent).toContain('nvidia.com/gpu=true:NoSchedule');
		expect(container.textContent).toContain('node-address-1');
		expect(screen.getByRole('tab', { name: 'YAML' })).toBeTruthy();
	});

	it('says why use and pods are missing and shows no zero in their place', async () => {
		vi.stubGlobal(
			'fetch',
			answer({
				node,
				metrics: { available: false, reason: 'Node metrics (metrics.k8s.io) is not served by this cluster' },
				pods: { available: false, reason: 'You do not have permission to see this.' }
			})
		);
		const { container } = render(Page);
		await waitFor(() => expect(screen.getByTestId('capacity')).toBeTruthy());
		expect(container.textContent).toContain('CPU and memory use are not shown: Node metrics (metrics.k8s.io) is not served');
		expect(container.textContent).toContain('Pods are not shown: You do not have permission');
		expect(screen.getByTestId('capacity').textContent).not.toMatch(/\(\d+%\)/);
		expect(screen.queryByTestId('pods')).toBeNull();
	});

	it('shows the error the API gave when the node cannot be read', async () => {
		vi.stubGlobal('fetch', answer({ error: 'You do not have permission to see this.' }, 403));
		render(Page);
		await waitFor(() => expect(screen.getByText(/do not have permission/)).toBeTruthy());
	});
});
