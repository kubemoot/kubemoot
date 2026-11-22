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
		const s = summarizeNode(node('n1'), { cpu: '2', memory: '12Gi' }, { running: 20, total: 22, accelerators: 0 }, true);
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

	it('says why a node is not healthy and marks high use', () => {
		const down = summarizeNode(node('n1', { conditions: [{ type: 'Ready', status: 'False', reason: 'KubeletNotReady' }] }), undefined, undefined, false);
		expect(down).toMatchObject({ health: 'down', problems: ['Not Ready (KubeletNotReady)'] });
		const hot = summarizeNode(node('n1'), { cpu: '7.9', memory: '1Gi' }, undefined, false);
		expect(hot.cpu?.level).toBe('critical');
		expect(hot.problems).toEqual(['CPU nearly all in use']);
	});

	it('shows accelerator use as devices asked for by pods against devices the node has', () => {
		const s = summarizeNode(node('g1', {}, { present: true, type: 'RTX 5090', count: 2, memory: '32GB' }), undefined, { running: 1, total: 1, accelerators: 1 }, true);
		expect(s.accelerator).toEqual({ label: 'RTX 5090 x2 32GB', use: { label: 'In use', text: '1 of 2', fraction: 0.5, level: 'normal' } });
	});

	it('names the accelerator without use when pods could not be read', () => {
		const s = summarizeNode(node('g1', {}, { present: true, type: 'RTX 5090', count: 1 }), undefined, undefined, false);
		expect(s.accelerator).toEqual({ label: 'RTX 5090' });
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

describe('accelerator use is never drawn as a warning', () => {
	it('keeps a fully used accelerator at the normal level, since using it is what it is for', () => {
		const s = summarizeNode(node('g1', {}, { present: true, type: 'X', count: 1 }), undefined, { running: 1, total: 1, accelerators: 1 }, true);
		expect(s.accelerator?.use).toMatchObject({ fraction: 1, level: 'normal' });
	});
});
