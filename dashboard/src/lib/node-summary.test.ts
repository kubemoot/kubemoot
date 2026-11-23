import { describe, expect, it } from 'vitest';
import { bySeverityThenName, summarizeNode } from './node-summary';
import { bar } from './usage-bar';
import { linkHref } from './overview-link';

const node = (name: string, over: object = {}, accelerator?: object) =>
	({
		metadata: { name },
		spec: {},
		status: {
			conditions: [{ type: 'Ready', status: 'True' }],
			allocatable: { cpu: '8', memory: '16Gi', pods: '110' },
			capacity: { cpu: '8', memory: '16Gi', pods: '110' },
			...over
		},
		accelerator
	}) as never;

describe('summarizeNode', () => {
	it('shows CPU, memory and pod room against what the node offers', () => {
		const s = summarizeNode(node('n1'), { cpu: '2', memory: '12Gi' }, { running: 20, total: 22, accelerators: 0, holders: [] }, true);
		expect(s.health).toBe('healthy');
		expect(s.cpu).toEqual({ label: 'CPU', text: '2 of 8 cores', fraction: 0.25, level: 'normal' });
		expect(s.memory).toMatchObject({ text: '12 GiB of 16 GiB', fraction: 0.75 });
		expect(s.pods).toMatchObject({ text: '20 running, 22 of 110', fraction: 0.2 });
	});

	it('leaves out a bar whose reading is missing rather than drawing zero', () => {
		const s = summarizeNode(node('n1'), undefined, undefined, false);
		expect(s.cpu).toBeUndefined();
		expect(s.memory).toBeUndefined();
		expect(s.pods).toBeUndefined();
	});

	it('shows a cordoned node that is not Ready as offline, not as a fault', () => {
		const s = summarizeNode(node('g3', { conditions: [{ type: 'Ready', status: 'Unknown' }] }), undefined, undefined, false);
		expect(s).toMatchObject({ health: 'down', offline: false });
		const off = summarizeNode({ ...(node('g3', { conditions: [{ type: 'Ready', status: 'Unknown' }] }) as object), spec: { unschedulable: true } } as never, undefined, undefined, false);
		expect(off).toMatchObject({ health: 'idle', offline: true, problems: ['Offline (cordoned)'] });
	});

	it('says why a node is not healthy and marks high use', () => {
		const down = summarizeNode(node('n1', { conditions: [{ type: 'Ready', status: 'False', reason: 'KubeletNotReady' }] }), undefined, undefined, false);
		expect(down).toMatchObject({ health: 'down', problems: ['Not Ready (KubeletNotReady)'] });
		const hot = summarizeNode(node('n1'), { cpu: '7.9', memory: '1Gi' }, undefined, false);
		expect(hot.cpu?.level).toBe('critical');
		expect(hot.problems).toEqual(['CPU nearly all in use']);
	});

	it('names the accelerator and says what holds it, with no allocation bar', () => {
		const servers = [{ metadata: { name: 'vllm-rig1', namespace: 'kubemoot' }, spec: { type: 'vllm', serviceRef: { name: 'vllm-rig1-kubemoot-vllm', namespace: 'vllm' } }, status: { capacity: { loadedModels: [{ name: 'qwen3:14b' }] } } }] as never;
		const pods = { running: 1, total: 1, accelerators: 1, holders: [{ namespace: 'vllm', name: 'vllm-rig1-kubemoot-vllm-67875d75c6-lhphb' }] };
		const s = summarizeNode(node('g1', {}, { present: true, type: 'NVIDIA-GeForce-RTX-5090', count: 2, memory: '32GB' }), undefined, pods, true, { servers });
		expect(s.accelerator).toEqual({ label: 'RTX 5090 x2 32GB', serving: { kind: 'server', name: 'vllm-rig1', namespace: 'kubemoot', models: ['qwen3:14b'] } });
		expect(JSON.stringify(s)).not.toContain('In use');
	});

	it('names the accelerator and nothing about what holds it when pods could not be read', () => {
		const s = summarizeNode(node('g1', {}, { present: true, type: 'RTX 5090', count: 1 }), undefined, undefined, false, { servers: [] });
		expect(s.accelerator).toEqual({ label: 'RTX 5090' });
	});

	it('says an accelerator node that is not Ready is not available, with no bars and unknown pods', () => {
		const off = node('g3', { conditions: [{ type: 'Ready', status: 'Unknown' }] }, { present: true, type: 'RTX 4090', count: 1, memory: '24GB' });
		const use = { g3: { devices: 1, busy: 0.4, memoryUsedMiB: 1, memoryTotalMiB: 2 } };
		const s = summarizeNode(off, undefined, { running: 6, total: 9, accelerators: 0, holders: [] }, true, { servers: [], use });
		expect(s.accelerator).toEqual({ label: 'RTX 4090 24GB', unavailable: 'Not available to Kubemoot: node offline' });
		expect(s.pods).toBeUndefined();
		expect(s.podsNote).toBe('unknown while the node is offline');
	});

	it('has no accelerator row on a node without one', () => {
		expect(summarizeNode(node('n1', {}, { present: false }), undefined, undefined, false).accelerator).toBeUndefined();
	});
});

describe('bySeverityThenName', () => {
	it('puts the nodes that need a look first, then sorts by name', () => {
		const down = summarizeNode(node('z', { conditions: [{ type: 'Ready', status: 'False' }] }), undefined, undefined, false);
		const cordoned = summarizeNode(node('m', {}), undefined, undefined, false);
		cordoned.health = 'degraded';
		const a = summarizeNode(node('a'), undefined, undefined, false);
		const b = summarizeNode(node('b'), undefined, undefined, false);
		expect([b, a, cordoned, down].sort(bySeverityThenName).map((s) => s.name)).toEqual(['z', 'm', 'a', 'b']);
	});
});

describe('bar', () => {
	it('keeps the share between 0 and 1 and sets the level', () => {
		expect(bar('x', 't', 1.4)).toMatchObject({ fraction: 1, level: 'critical' });
		expect(bar('x', 't', -1)).toMatchObject({ fraction: 0, level: 'normal' });
		expect(bar('x', 't', Number.NaN).fraction).toBe(0);
		expect(bar('x', 't', 0.92).level).toBe('warning');
	});
});

describe('linkHref', () => {
	it('adds the base path and the query to the path', () => {
		expect(linkHref({ path: '/nodes/n1' }, '/dashboard/')).toBe('/dashboard/nodes/n1');
		expect(linkHref({ path: '/crews/a', search: '?namespace=x' }, '/')).toBe('/crews/a?namespace=x');
	});
});

describe('measured accelerator use', () => {
	const gpu = { present: true, type: 'RTX 5090', count: 1 };
	const idle = { running: 1, total: 1, accelerators: 0, holders: [] };

	it('draws busy and memory bars from the metrics source, never as a warning even when full', () => {
		const use = { g1: { devices: 1, busy: 1, memoryUsedMiB: 32000, memoryTotalMiB: 32000 } };
		const s = summarizeNode(node('g1', {}, gpu), undefined, idle, true, { servers: [], use });
		expect(s.accelerator?.busy).toMatchObject({ label: 'Busy', fraction: 1, level: 'normal' });
		expect(s.accelerator?.memory).toMatchObject({ label: 'Memory', text: '31.3 GiB of 31.3 GiB', fraction: 1, level: 'normal' });
		expect(s.accelerator?.serving).toEqual({ kind: 'free' });
	});

	it('draws no bars, and no zero, when no metrics source is set', () => {
		const s = summarizeNode(node('g1', {}, gpu), undefined, idle, true, { servers: [] });
		expect(s.accelerator?.busy).toBeUndefined();
		expect(s.accelerator?.memory).toBeUndefined();
	});

	it('draws a bar only for the measure the source reports', () => {
		const s = summarizeNode(node('g1', {}, gpu), undefined, idle, true, { servers: [], use: { g1: { devices: 1, busy: 0.25 } } });
		expect(s.accelerator?.busy).toMatchObject({ fraction: 0.25 });
		expect(s.accelerator?.memory).toBeUndefined();
	});
});
