// What someone diagnosing a node reads: whether it is Ready, which pressures it reports, and how
// much of its CPU and memory is in use. Pure functions over the Node and the metrics reading.

import type { Health } from './health.js';
import type { NodeCondition } from './types/k8s.js';
import { HIGH_USE_FRACTION } from './usage-bar.js';

/** Conditions that mean the node is struggling when True (Ready is the opposite: it is bad when not True). */
const PRESSURE_TYPES = ['MemoryPressure', 'DiskPressure', 'PIDPressure', 'NetworkUnavailable'] as const;


const BINARY: Record<string, number> = { Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4, Pi: 1024 ** 5, Ei: 1024 ** 6 };
const DECIMAL: Record<string, number> = { n: 1e-9, u: 1e-6, m: 1e-3, '': 1, k: 1e3, K: 1e3, M: 1e6, G: 1e9, T: 1e12, P: 1e15, E: 1e18 };

/** A Kubernetes quantity ("250m", "16Gi", "123456Ki", "8") as a plain number; undefined when unreadable. */
export function parseQuantity(text: string | undefined | null): number | undefined {
	if (typeof text !== 'string' || text.trim() === '') return undefined;
	const trimmed = text.trim();
	const at = trimmed.search(/[A-Za-z]/);
	const unit = at < 0 ? '' : trimmed.slice(at);
	const number = Number(at < 0 ? trimmed : trimmed.slice(0, at));
	const factor = BINARY[unit] ?? DECIMAL[unit];
	return Number.isNaN(number) || factor === undefined ? undefined : number * factor;
}

/** One resource's use against what the node can give. */
export interface Usage {
	used: number;
	allocatable: number;
	/** used / allocatable; always finite, clamped to at least 0. */
	fraction: number;
}

/** The use of one resource; undefined when either side is unknown or the node offers none. */
export function usageOf(usedText: string | undefined, allocatableText: string | undefined): Usage | undefined {
	const used = parseQuantity(usedText);
	const allocatable = parseQuantity(allocatableText);
	if (used === undefined || allocatable === undefined || allocatable <= 0) return undefined;
	return { used, allocatable, fraction: Math.max(0, used / allocatable) };
}

/** A node's CPU (in cores) and memory (in bytes) use. */
export interface NodeUse {
	cpu?: Usage;
	memory?: Usage;
}

/** What metrics.k8s.io reports for a node: its usage quantities. */
export interface NodeMetric {
	cpu?: string;
	memory?: string;
}

/** A node's CPU and memory use against its allocatable. */
export function nodeUse(
	node: { status?: { allocatable?: Record<string, string> } },
	metric: NodeMetric | undefined
): NodeUse {
	if (!metric) return {};
	const allocatable = node.status?.allocatable;
	return {
		cpu: usageOf(metric.cpu, allocatable?.cpu),
		memory: usageOf(metric.memory, allocatable?.memory)
	};
}

/** The whole cluster's use: the sum of use over the sum of allocatable of the nodes that report both. */
export function clusterUse(
	nodes: readonly { status?: { allocatable?: Record<string, string> }; metadata: { name: string } }[],
	metrics: Readonly<Record<string, NodeMetric>>
): NodeUse {
	const sum = (pick: (u: NodeUse) => Usage | undefined): Usage | undefined => {
		let used = 0;
		let allocatable = 0;
		for (const node of nodes) {
			const u = pick(nodeUse(node, metrics[node.metadata.name]));
			if (!u) continue;
			used += u.used;
			allocatable += u.allocatable;
		}
		return allocatable > 0 ? { used, allocatable, fraction: used / allocatable } : undefined;
	};
	return { cpu: sum((u) => u.cpu), memory: sum((u) => u.memory) };
}

type NodeLike = {
	spec?: { unschedulable?: boolean };
	status?: { conditions?: NodeCondition[] };
};

function condition(node: NodeLike, type: string): NodeCondition | undefined {
	return node.status?.conditions?.find((c) => c.type === type);
}

/** True when the node reports Ready=True. */
export function isNodeReady(node: NodeLike): boolean {
	return condition(node, 'Ready')?.status === 'True';
}

/**
 * Whether a condition is a problem now: Ready is one when it is not True; a pressure or unavailable
 * condition is one when it is True. An Unknown pressure says nothing by itself; the node's Ready
 * condition carries that case.
 */
export function isConditionProblem(c: Pick<NodeCondition, 'type' | 'status'>): boolean {
	return c.type === 'Ready' ? c.status !== 'True' : c.status === 'True';
}

/** The pressure conditions that are a problem on the node, such as MemoryPressure. */
export function pressures(node: NodeLike): NodeCondition[] {
	return PRESSURE_TYPES.flatMap((type) => {
		const c = condition(node, type);
		return c && isConditionProblem(c) ? [c] : [];
	});
}

/** One reason a node is not healthy, in words a person reads. */
export interface NodeProblem {
	/** Idle is a node taken out on purpose: nothing is wrong. */
	health: 'down' | 'degraded' | 'idle';
	text: string;
}

function readinessProblems(node: NodeLike): NodeProblem[] {
	if (isNodeReady(node)) return [];
	const reason = condition(node, 'Ready')?.reason;
	return [{ health: 'down', text: reason ? `Not Ready (${reason})` : 'Not Ready' }];
}

function useProblems(use: NodeUse): NodeProblem[] {
	const high = (u: Usage | undefined) => u !== undefined && u.fraction >= HIGH_USE_FRACTION;
	return [
		...(high(use.cpu) ? [{ health: 'degraded' as const, text: 'CPU nearly all in use' }] : []),
		...(high(use.memory) ? [{ health: 'degraded' as const, text: 'Memory nearly all in use' }] : [])
	];
}

/** A node that is cordoned and not Ready was taken out on purpose, such as an accelerator node whose VM is off. */
export function isOffline(node: NodeLike): boolean {
	return node.spec?.unschedulable === true && !isNodeReady(node);
}

/**
 * Why a node is not healthy: not Ready, a pressure, cordoned, or CPU or memory nearly all in use.
 * Empty when healthy. A cordoned node that is not Ready is offline on purpose: its one entry is idle.
 */
export function nodeProblems(node: NodeLike, use: NodeUse = {}): NodeProblem[] {
	if (isOffline(node)) return [{ health: 'idle', text: 'Offline (cordoned)' }];
	return [
		...readinessProblems(node),
		...pressures(node).map((c) => ({ health: 'degraded' as const, text: c.type })),
		...(node.spec?.unschedulable ? [{ health: 'degraded' as const, text: 'Cordoned, no new pods' }] : []),
		...useProblems(use)
	];
}

/** The node's health: down when not Ready, degraded for any other problem, idle when offline on purpose, else healthy. */
export function nodeHealth(node: NodeLike, use: NodeUse = {}): Health {
	const problems = nodeProblems(node, use);
	if (problems.some((p) => p.health === 'down')) return 'down';
	if (problems.some((p) => p.health === 'degraded')) return 'degraded';
	return problems.length > 0 ? 'idle' : 'healthy';
}

/** CPU in cores as a bare number, for a bar: "1.5". */
export const cores = (n: number): string => `${+n.toFixed(1)}`;

/** Memory in bytes as "15.1 GiB". */
export const gib = (n: number): string => `${+(n / 1024 ** 3).toFixed(1)} GiB`;

/** CPU in cores as "1.5 cores" or "250m", for the capacity table. */
export function formatCores(cores: number): string {
	if (cores >= 1) return `${+cores.toFixed(1)} cores`;
	return `${Math.round(cores * 1000)}m`;
}

/** A use as a whole percent, "62%". */
export function formatUse(fraction: number): string {
	return `${Math.round(fraction * 100)}%`;
}
