import { describe, expect, it } from 'vitest';
import { clusterUse, formatCores, formatUse, isNodeReady, nodeHealth, nodeProblems, nodeUse, parseQuantity, pressures, usageOf } from './node-usage';

const ready = { type: 'Ready', status: 'True' as const };
const node = (over: object = {}, conditions: { type: string; status: 'True' | 'False' | 'Unknown'; reason?: string }[] = [ready]) => ({
	metadata: { name: 'n1' },
	status: { conditions, allocatable: { cpu: '8', memory: '16Gi' } },
	...over
});

describe('parseQuantity', () => {
	it('reads Kubernetes quantities', () => {
		expect(parseQuantity('8')).toBe(8);
		expect(parseQuantity('250m')).toBeCloseTo(0.25);
		expect(parseQuantity('16Gi')).toBe(16 * 1024 ** 3);
		expect(parseQuantity('1024Ki')).toBe(1024 ** 2);
		expect(parseQuantity('3500000n')).toBeCloseTo(0.0035);
		expect(parseQuantity('1.5')).toBe(1.5);
		expect(parseQuantity('2k')).toBe(2000);
	});

	it('is undefined for what it cannot read, never zero', () => {
		for (const bad of [undefined, null, '', ' ', 'abc', '12Xi', '1.2.3Gi']) expect(parseQuantity(bad as string)).toBeUndefined();
	});
});

describe('usageOf', () => {
	it('is the share of allocatable in use', () => {
		expect(usageOf('4', '8')).toEqual({ used: 4, allocatable: 8, fraction: 0.5 });
		expect(usageOf('8Gi', '16Gi')?.fraction).toBe(0.5);
	});

	it('is undefined when either side is unknown or the node offers none', () => {
		expect(usageOf(undefined, '8')).toBeUndefined();
		expect(usageOf('4', undefined)).toBeUndefined();
		expect(usageOf('4', '0')).toBeUndefined();
		expect(usageOf('x', '8')).toBeUndefined();
	});

	it('shows use above allocatable as over one, not clipped', () => {
		expect(usageOf('9', '8')?.fraction).toBeGreaterThan(1);
	});
});

describe('nodeUse and clusterUse', () => {
	it('measures CPU and memory against the node allocatable', () => {
		const use = nodeUse(node(), { cpu: '2', memory: '4Gi' });
		expect(use.cpu?.fraction).toBe(0.25);
		expect(use.memory?.fraction).toBe(0.25);
	});

	it('has no use without a reading', () => {
		expect(nodeUse(node(), undefined)).toEqual({});
	});

	it('sums use over allocatable for the cluster, leaving out nodes with no reading', () => {
		const nodes = [node(), { ...node(), metadata: { name: 'n2' } }, { ...node(), metadata: { name: 'n3' } }];
		const use = clusterUse(nodes, { n1: { cpu: '4', memory: '8Gi' }, n2: { cpu: '2', memory: '8Gi' } });
		expect(use.cpu).toMatchObject({ used: 6, allocatable: 16, fraction: 0.375 });
		expect(use.memory?.fraction).toBe(0.5);
	});

	it('has no cluster use when no node reports', () => {
		expect(clusterUse([node()], {})).toEqual({ cpu: undefined, memory: undefined });
	});
});

describe('node conditions', () => {
	it('is Ready only when Ready is True', () => {
		expect(isNodeReady(node())).toBe(true);
		expect(isNodeReady(node({}, [{ type: 'Ready', status: 'Unknown' }]))).toBe(false);
		expect(isNodeReady(node({}, []))).toBe(false);
	});

	it('lists the pressures that are True', () => {
		const n = node({}, [ready, { type: 'MemoryPressure', status: 'True' }, { type: 'DiskPressure', status: 'False' }]);
		expect(pressures(n).map((c) => c.type)).toEqual(['MemoryPressure']);
	});

	it('is healthy with no problems', () => {
		expect(nodeProblems(node())).toEqual([]);
		expect(nodeHealth(node())).toBe('healthy');
	});

	it('is down when not Ready, with the reason', () => {
		const n = node({}, [{ type: 'Ready', status: 'False', reason: 'KubeletNotReady' }]);
		expect(nodeProblems(n)).toEqual([{ health: 'down', text: 'Not Ready (KubeletNotReady)' }]);
		expect(nodeHealth(n)).toBe('down');
	});

	it('is degraded under pressure, cordoned, or with CPU or memory nearly all in use', () => {
		expect(nodeHealth(node({}, [ready, { type: 'DiskPressure', status: 'True' }]))).toBe('degraded');
		expect(nodeHealth(node({ spec: { unschedulable: true } }))).toBe('degraded');
		const hot = nodeUse(node(), { cpu: '7.5', memory: '1Gi' });
		expect(nodeProblems(node(), hot).map((p) => p.text)).toEqual(['CPU nearly all in use']);
		expect(nodeHealth(node(), hot)).toBe('degraded');
	});

	it('stays down when not Ready and also under pressure', () => {
		const n = node({}, [{ type: 'Ready', status: 'False' }, { type: 'MemoryPressure', status: 'True' }]);
		expect(nodeHealth(n)).toBe('down');
		expect(nodeProblems(n)).toHaveLength(2);
	});
});

describe('formatting', () => {
	it('shows cores and percent', () => {
		expect(formatCores(1.5)).toBe('1.5 cores');
		expect(formatCores(0.25)).toBe('250m');
		expect(formatUse(0.624)).toBe('62%');
	});
});
