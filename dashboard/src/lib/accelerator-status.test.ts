import { describe, expect, it } from 'vitest';
import { acceleratorStatus, combinedUse, nodeOfServer, podServes, servingOn, serverAccelerator, useBars } from './accelerator-status';

const server = (name: string, spec: object, loaded: string[] = [], capacity: object = {}) =>
	({ metadata: { name, namespace: 'kubemoot' }, spec: { type: 'vllm', ...spec }, status: { capacity: { loadedModels: loaded.map((n) => ({ name: n })), ...capacity } } }) as never;

const rig1 = server('vllm-rig1', { serviceRef: { name: 'vllm-rig1-kubemoot-vllm', namespace: 'vllm' } }, ['qwen3:14b']);
const cpu = server('vllm-cpu', { endpoint: 'https://vllm-cpu.vllm:8000' }, ['nomic-embed-text']);
const pod = (name: string, namespace = 'vllm') => ({ namespace, name });

describe('which model server holds an accelerator', () => {
	it('matches a pod behind the serviceRef Service', () => {
		expect(podServes(pod('vllm-rig1-kubemoot-vllm-67875d75c6-lhphb'), rig1)).toBe(true);
	});

	it('matches a pod behind the endpoint host', () => {
		expect(podServes(pod('vllm-cpu-58b4b85848-tf624'), cpu)).toBe(true);
	});

	it('does not match another namespace, another workload with a longer name, or a name that only starts the same', () => {
		expect(podServes(pod('vllm-rig1-kubemoot-vllm-67875d75c6-lhphb', 'other'), rig1)).toBe(false);
		expect(podServes(pod('vllm-rig1-kubemoot-vllm-extra-67875d75c6-lhphb'), rig1)).toBe(false);
		expect(podServes(pod('vllm-rig1-kubemoot-vllmx'), rig1)).toBe(false);
		expect(podServes(pod('vllm-rig1-kubemoot-vllm-67875d75c6-lhphb'), server('x', {}))).toBe(false);
	});

	it('answers server and loaded models, as in "vllm-rig1 qwen3:14b"', () => {
		expect(servingOn('n', [pod('vllm-rig1-kubemoot-vllm-67875d75c6-lhphb')], [cpu, rig1])).toEqual({ kind: 'server', name: 'vllm-rig1', namespace: 'kubemoot', models: ['qwen3:14b'] });
	});

	it('says a server with no model loaded holds none', () => {
		const idle = server('vllm-rig1', { serviceRef: { name: 'vllm-rig1-kubemoot-vllm', namespace: 'vllm' } });
		expect(servingOn('n', [pod('vllm-rig1-kubemoot-vllm-67875d75c6-lhphb')], [idle])).toMatchObject({ kind: 'server', models: [] });
	});

	it('matches by the node a server reports when the pods do not tell', () => {
		const named = server('s', {}, ['m'], { nodeName: 'n1' });
		expect(servingOn('n1', [pod('anything')], [named])).toMatchObject({ kind: 'server', name: 's' });
	});

	it('says Held by the pod when no Kubemoot model server holds the device, and Free when no pod does', () => {
		expect(servingOn('n', [pod('trainer-abcde')], [rig1])).toEqual({ kind: 'pod', pod: 'trainer-abcde' });
		expect(servingOn('n', [], [rig1])).toEqual({ kind: 'free' });
	});

	it('finds the node of a server from the pods that hold accelerators', () => {
		const by = { a: [pod('x-1')], b: [pod('vllm-rig1-kubemoot-vllm-67875d75c6-lhphb')] };
		expect(nodeOfServer(rig1, by)).toBe('b');
		expect(nodeOfServer(rig1, { a: [pod('x-1')] })).toBeUndefined();
		expect(nodeOfServer(server('s', {}, [], { nodeName: 'n9' }), {})).toBe('n9');
	});
});

describe('useBars', () => {
	it('is empty when nothing was measured', () => {
		expect(useBars(undefined)).toEqual({});
		expect(useBars({ devices: 1 })).toEqual({ busy: undefined, memory: undefined });
	});

	it('draws busy and memory that never warn', () => {
		const { busy, memory } = useBars({ devices: 1, busy: 0.99, memoryUsedMiB: 23000, memoryTotalMiB: 24000 });
		expect(busy).toMatchObject({ label: 'Busy', fraction: 0.99, level: 'normal' });
		expect(memory?.level).toBe('normal');
	});

	it('draws no memory bar for a zero total', () => {
		expect(useBars({ devices: 1, memoryUsedMiB: 0, memoryTotalMiB: 0 }).memory).toBeUndefined();
	});
});

describe('acceleratorStatus', () => {
	const gpu = (ready: boolean) => ({ metadata: { name: 'g' }, status: { conditions: [{ type: 'Ready', status: ready ? 'True' : 'False' }] }, accelerator: { present: true, type: 'NVIDIA-GeForce-RTX-4090', count: 1, memory: '24GB' } }) as never;

	it('is undefined on a node without an accelerator', () => {
		expect(acceleratorStatus({ node: { metadata: { name: 'c' }, accelerator: { present: false } } as never })).toBeUndefined();
	});

	it('is unavailable on a node that is not Ready, with no serving and no bars even when metrics linger', () => {
		const s = acceleratorStatus({ node: gpu(false), holders: [], servers: [], use: { g: { devices: 1, busy: 0.5 } } });
		expect(s).toEqual({ label: 'RTX 4090 24GB', unavailable: 'Not available to Kubemoot: node offline' });
	});

	it('says nothing about what holds it until the model servers are known', () => {
		expect(acceleratorStatus({ node: gpu(true), holders: [] })?.serving).toBeUndefined();
	});
});

describe('combinedUse', () => {
	const g = (name: string, ready = true) => ({ metadata: { name }, status: { conditions: [{ type: 'Ready', status: ready ? 'True' : 'False' }] }, accelerator: { present: true } }) as never;

	it('averages busy over devices and sums memory over Ready accelerator nodes only', () => {
		const use = { a: { devices: 1, busy: 1, memoryUsedMiB: 10, memoryTotalMiB: 20 }, b: { devices: 3, busy: 0, memoryUsedMiB: 5, memoryTotalMiB: 40 }, c: { devices: 1, busy: 0.5, memoryUsedMiB: 99, memoryTotalMiB: 99 } };
		expect(combinedUse([g('a'), g('b'), g('c', false)], use)).toEqual({ devices: 4, busy: 0.25, memoryUsedMiB: 15, memoryTotalMiB: 60 });
	});

	it('is undefined with no source or no measured node', () => {
		expect(combinedUse([g('a')], undefined)).toBeUndefined();
		expect(combinedUse([g('a')], {})).toBeUndefined();
	});

	it('leaves out a measure that one node did not report', () => {
		const out = combinedUse([g('a'), g('b')], { a: { devices: 1, busy: 1 }, b: { devices: 1, memoryUsedMiB: 1, memoryTotalMiB: 2 } });
		expect(out).toMatchObject({ busy: undefined, memoryUsedMiB: undefined });
	});
});

describe('serverAccelerator', () => {
	const nodes = [{ metadata: { name: 'g' }, status: { conditions: [{ type: 'Ready', status: 'True' }] }, accelerator: { present: true, type: 'RTX 5090', count: 1 } }] as never;
	const holders = { g: [pod('vllm-rig1-kubemoot-vllm-67875d75c6-lhphb')] };

	it('gives the node, accelerator and measured bars of the server', () => {
		const out = serverAccelerator(rig1, nodes, holders, { g: { devices: 1, busy: 0.2 } });
		expect(out).toMatchObject({ node: 'g', label: 'RTX 5090', busy: { fraction: 0.2 } });
	});

	it('is undefined when the node cannot be told', () => {
		expect(serverAccelerator(rig1, nodes, {}, undefined)).toBeUndefined();
	});
});
