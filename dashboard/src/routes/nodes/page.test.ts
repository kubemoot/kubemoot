import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPageUrl } from '../../../vitest-mocks/app-state.svelte.js';
import Page from './+page.svelte';

const ready = [{ type: 'Ready', status: 'True', reason: 'KubeletReady' }];
const node = (name: string, labels: Record<string, string> = {}, accelerator = false) => ({
	apiVersion: 'v1',
	kind: 'Node',
	metadata: { name, labels },
	spec: {},
	status: { conditions: ready, allocatable: { cpu: '8', memory: '16Gi', pods: '110' } },
	accelerator: accelerator ? { present: true, count: 1, type: 'GPU' } : { present: false }
});

const nodes = [
	node('cp-1', { 'node-role.kubernetes.io/control-plane': '' }),
	node('worker-1'),
	node('gpu-1', {}, true)
];

beforeEach(() => {
	localStorage.clear();
	setPageUrl('/dashboard/nodes');
	page.params = {};
	vi.stubGlobal(
		'fetch',
		vi.fn(async (url: string) => {
			const body = String(url).includes('node-usage') ? { metrics: { available: false, reason: 'not served' }, pods: { available: false, reason: 'not served' } } : { nodes };
			return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
		})
	);
});
afterEach(() => vi.unstubAllGlobals());

const listed = () => ['cp-1', 'worker-1', 'gpu-1'].filter((n) => screen.queryByTestId(`node-${n}`));
const picker = () => screen.getByRole<HTMLSelectElement>('combobox', { name: 'Show nodes' });

describe('the Nodes page filter', () => {
	it('offers All, Worker nodes, Accelerator nodes and Control plane nodes, and opens on All', async () => {
		render(Page);
		await waitFor(() => expect(listed()).toHaveLength(3));
		expect([...picker().options].map((o) => o.textContent)).toEqual(['All', 'Worker nodes', 'Accelerator nodes', 'Control plane nodes']);
		expect(picker().value).toBe('all');
	});

	it.each([
		['all', ['cp-1', 'worker-1', 'gpu-1']],
		['worker', ['worker-1', 'gpu-1']],
		['accelerator', ['gpu-1']],
		['control-plane', ['cp-1']]
	])('?role=%s lists %j', async (role, expected) => {
		setPageUrl(`/dashboard/nodes?role=${role}`);
		render(Page);
		await waitFor(() => expect(listed()).toEqual(expected));
		expect(picker().value).toBe(role);
	});

	it('keeps the choice in the address and in this browser', async () => {
		render(Page);
		await waitFor(() => expect(listed()).toHaveLength(3));
		await fireEvent.change(picker(), { target: { value: 'accelerator' } });
		await waitFor(() => expect(listed()).toEqual(['gpu-1']));
		expect(page.url.searchParams.get('role')).toBe('accelerator');
		expect(localStorage.getItem('kubemoot-dashboard-nodes-role')).toBe('accelerator');
	});

	it('opens on the role this viewer chose last, and puts it in the address', async () => {
		localStorage.setItem('kubemoot-dashboard-nodes-role', 'control-plane');
		render(Page);
		await waitFor(() => expect(listed()).toEqual(['cp-1']));
		expect(page.url.searchParams.get('role')).toBe('control-plane');
	});

	it('lets the address win over the remembered role, so a tile link always shows what it counts', async () => {
		localStorage.setItem('kubemoot-dashboard-nodes-role', 'accelerator');
		setPageUrl('/dashboard/nodes?role=all');
		render(Page);
		await waitFor(() => expect(listed()).toHaveLength(3));
		expect(picker().value).toBe('all');
	});

	it('ignores a role in the address that is not one, and keeps working when storage refuses', async () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			}
		});
		setPageUrl('/dashboard/nodes?role=leader');
		render(Page);
		await waitFor(() => expect(listed()).toHaveLength(3));
		await fireEvent.change(picker(), { target: { value: 'worker' } });
		await waitFor(() => expect(listed()).toEqual(['worker-1', 'gpu-1']));
	});

	it('says which kind of node is missing when there are none of it', async () => {
		setPageUrl('/dashboard/nodes?role=control-plane');
		vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ nodes: [node('worker-1')], metrics: { available: false, reason: 'x' }, pods: { available: false, reason: 'x' } }), { status: 200 })));
		render(Page);
		await waitFor(() => expect(screen.getByText('No control plane nodes in the cluster.')).toBeTruthy());
	});
});

describe('accelerator tiles on the Nodes page', () => {
	const gpu = (name: string, ready: boolean) => ({
		...node(name, {}, true),
		spec: ready ? {} : { unschedulable: true },
		status: { conditions: [{ type: 'Ready', status: ready ? 'True' : 'Unknown' }], allocatable: { cpu: '8', memory: '16Gi', pods: '110' } }
	});
	const rig1Server = { metadata: { name: 'vllm-rig1', namespace: 'kubemoot' }, spec: { type: 'vllm', serviceRef: { name: 'vllm-rig1-x', namespace: 'vllm' } }, status: { capacity: { loadedModels: [{ name: 'qwen3:14b' }] } } };
	const serve = (use: object, pods: object) => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async (url: string) => {
				const u = String(url);
				let body: object = { nodes: [gpu('rig1', true), gpu('rig3', false)] };
				if (u.includes('node-usage')) body = { metrics: { available: false, reason: 'not served' }, pods };
				if (u.includes('modelservers')) body = { items: [rig1Server] };
				if (u.includes('accelerator-use')) body = use;
				return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
			})
		);
	};
	const podsRead = { available: true, data: { rig1: { running: 1, total: 1, accelerators: 1, holders: [{ namespace: 'vllm', name: 'vllm-rig1-x-6d5f8c7b9d-abcde' }] }, rig3: { running: 6, total: 9, accelerators: 0, holders: [] } } };

	it('says what serves the accelerator, leaves out busy and memory when no source is set, and shows no allocation bar', async () => {
		serve({ configured: false }, podsRead);
		render(Page);
		await waitFor(() => expect(screen.getByTestId('node-rig1').textContent).toContain('vllm-rig1'));
		const tile = screen.getByTestId('node-rig1').textContent ?? '';
		expect(tile).toContain('qwen3:14b');
		expect(tile).not.toContain('In use');
		expect(tile).not.toContain('Busy');
		expect(tile).not.toContain('Memory');
	});

	it('draws busy and memory when the source reports them', async () => {
		serve({ configured: true, use: { available: true, data: { rig1: { devices: 1, busy: 0.4, memoryUsedMiB: 8192, memoryTotalMiB: 16384 } } } }, podsRead);
		render(Page);
		await waitFor(() => expect(screen.getByTestId('node-rig1').textContent).toContain('Busy'));
		expect(screen.getByTestId('node-rig1').textContent).toContain('8 GiB of 16 GiB');
	});

	it('says why busy and memory are missing when a configured source fails', async () => {
		serve({ configured: true, use: { available: false, reason: 'the metrics source could not be read (HTTP 503)' } }, podsRead);
		render(Page);
		await waitFor(() => expect(screen.getByText(/Accelerator busy and memory are not shown: the metrics source could not be read/)).toBeTruthy());
	});

	it('says an offline node is not available, with no bars and pods unknown rather than stale counts', async () => {
		serve({ configured: true, use: { available: true, data: { rig3: { devices: 1, busy: 0, memoryUsedMiB: 1, memoryTotalMiB: 2 } } } }, podsRead);
		render(Page);
		await waitFor(() => expect(screen.getByTestId('node-rig3').textContent).toContain('Not available to Kubemoot: node offline'));
		const tile = screen.getByTestId('node-rig3');
		await waitFor(() => expect(tile.textContent).toContain('Pods: unknown while the node is offline'));
		expect(tile.textContent).not.toContain('running');
		expect(tile.querySelector('[role="meter"]')).toBeNull();
		expect(tile.textContent).not.toContain('In use');
	});
});
