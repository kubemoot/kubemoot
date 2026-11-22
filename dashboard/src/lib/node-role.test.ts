import { afterEach, describe, expect, it, vi } from 'vitest';
import { chosenRole, isControlPlane, loadRole, matchesRole, parseRole, roleSearch, saveRole, ROLE_OPTIONS } from './node-role';
import type { NodeWithAccelerator } from '#lib/types/k8s.js';

const node = (labels?: Record<string, string>, accelerator = false): NodeWithAccelerator =>
	({ metadata: { name: 'n', labels }, accelerator: { present: accelerator } }) as NodeWithAccelerator;

const control = node({ 'node-role.kubernetes.io/control-plane': '' });
const worker = node({ 'kubernetes.io/hostname': 'w' });
const gpuWorker = node({ 'node-role.kubernetes.io/worker': '' }, true);
const bare = node();
const gpuControl = node({ 'node-role.kubernetes.io/control-plane': '' }, true);

describe('matchesRole', () => {
	it('lists every node for All', () => {
		for (const n of [control, worker, gpuWorker, bare]) expect(matchesRole(n, 'all')).toBe(true);
	});

	it('takes the control-plane label as a control plane node, and every other node as a worker', () => {
		expect(isControlPlane(control)).toBe(true);
		expect(isControlPlane(worker)).toBe(false);
		expect(isControlPlane(bare)).toBe(false);
		expect(matchesRole(control, 'control-plane')).toBe(true);
		expect(matchesRole(worker, 'control-plane')).toBe(false);
		expect(matchesRole(control, 'worker')).toBe(false);
		expect(matchesRole(worker, 'worker')).toBe(true);
		expect(matchesRole(bare, 'worker')).toBe(true);
	});

	it('finds accelerator nodes by the devices they report, whatever the vendor or role', () => {
		expect(matchesRole(gpuWorker, 'accelerator')).toBe(true);
		expect(matchesRole(gpuControl, 'accelerator')).toBe(true);
		expect(matchesRole(worker, 'accelerator')).toBe(false);
		const old = { metadata: { name: 'o' }, gpu: { present: true } } as NodeWithAccelerator;
		expect(matchesRole(old, 'accelerator')).toBe(true);
	});
});

describe('parseRole and chosenRole', () => {
	it('knows the four roles and nothing else', () => {
		expect(ROLE_OPTIONS.map((o) => o.label)).toEqual(['All', 'Worker nodes', 'Accelerator nodes', 'Control plane nodes']);
		for (const o of ROLE_OPTIONS) expect(parseRole(o.id)).toBe(o.id);
		for (const v of ['leader', '', 'ALL', null, undefined, 'worker,all']) expect(parseRole(v)).toBeNull();
	});

	it('prefers the URL over the remembered role, and All over neither', () => {
		expect(chosenRole('accelerator', 'worker')).toBe('accelerator');
		expect(chosenRole('all', 'worker')).toBe('all');
		expect(chosenRole(null, 'worker')).toBe('worker');
		expect(chosenRole('bogus', 'worker')).toBe('worker');
		expect(chosenRole(null, null)).toBe('all');
	});

	it('writes the link query for a role', () => {
		expect(roleSearch('accelerator')).toBe('?role=accelerator');
		expect(roleSearch('control-plane')).toBe('?role=control-plane');
	});
});

describe('the remembered role', () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		localStorage.clear();
	});

	it('is kept and read back', () => {
		expect(loadRole()).toBeNull();
		saveRole('accelerator');
		expect(loadRole()).toBe('accelerator');
	});

	it('ignores a stored value that is not a role', () => {
		localStorage.setItem('kubemoot-dashboard-nodes-role', 'junk');
		expect(loadRole()).toBeNull();
	});

	it('does nothing when storage refuses', () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('full');
			}
		});
		expect(loadRole()).toBeNull();
		expect(() => saveRole('worker')).not.toThrow();
	});

	it('does nothing when there is no storage', () => {
		vi.stubGlobal('localStorage', undefined);
		expect(loadRole()).toBeNull();
		expect(() => saveRole('worker')).not.toThrow();
	});
});
