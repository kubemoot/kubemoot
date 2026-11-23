// The node's own page: conditions with their reasons, what the node has against what it offers
// against what is in use, and the facts that do not change.

import { formatCores, formatUse, gib, isConditionProblem, isNodeReady, parseQuantity, usageOf, type NodeMetric } from './node-usage.js';
import { PODS_OFFLINE_TEXT } from './accelerator-status.js';
import type { NodeCondition, NodeWithAccelerator } from './types/k8s.js';

/** A condition and whether it is a problem now. */
export interface ConditionRow extends NodeCondition {
	problem: boolean;
}

/** The node's conditions with the ones that are a problem first, then by type. */
export function conditionRows(node: Pick<NodeWithAccelerator, 'status'>): ConditionRow[] {
	const rows = (node.status?.conditions ?? []).map((c) => ({
		...c,
		problem: isConditionProblem(c)
	}));
	return rows.sort((a, b) => Number(b.problem) - Number(a.problem) || a.type.localeCompare(b.type));
}

export interface CapacityRow {
	resource: string;
	capacity: string;
	allocatable: string;
	/** What is in use; empty when it is not measured. */
	inUse: string;
}

const count = (n: number) => String(Math.round(n));

function row(resource: string, capacity: string | undefined, allocatable: string | undefined, format: (n: number) => string, inUse = ''): CapacityRow | undefined {
	const c = parseQuantity(capacity);
	const a = parseQuantity(allocatable);
	if (c === undefined && a === undefined) return undefined;
	return { resource, capacity: c === undefined ? '' : format(c), allocatable: a === undefined ? '' : format(a), inUse };
}

/** "1.5 cores (12%)"; empty when the use is not measured. */
function useText(used: ReturnType<typeof usageOf>, format: (n: number) => string): string {
	return used ? format(used.used) + ' (' + formatUse(used.fraction) + ')' : '';
}

/** Capacity against allocatable against use for CPU, memory, pods and storage, with the use only where it is measured. */
export function capacityRows(
	node: Pick<NodeWithAccelerator, 'status'>,
	metric: NodeMetric | undefined,
	podsRunning: number | undefined
): CapacityRow[] {
	const capacity = node.status?.capacity ?? {};
	const allocatable = node.status?.allocatable ?? {};
	const cpu = usageOf(metric?.cpu, allocatable.cpu);
	const memory = usageOf(metric?.memory, allocatable.memory);
	const pods = usageOf(podsRunning === undefined ? undefined : String(podsRunning), allocatable.pods);
	// A node that is not Ready reports pods it can no longer vouch for.
	const podsUse = isNodeReady(node) ? useText(pods, count) : PODS_OFFLINE_TEXT;
	return [
		row('CPU', capacity.cpu, allocatable.cpu, formatCores, useText(cpu, formatCores)),
		row('Memory', capacity.memory, allocatable.memory, gib, useText(memory, gib)),
		row('Pods', capacity.pods, allocatable.pods, count, podsUse),
		row('Ephemeral storage', capacity['ephemeral-storage'], allocatable['ephemeral-storage'], gib)
	].filter((r): r is CapacityRow => r !== undefined);
}

/** The fixed facts about the machine: what it runs and how to reach it. */
export function staticFacts(node: NodeWithAccelerator): { label: string; value: string }[] {
	const info = node.status?.nodeInfo;
	const address = (type: string) => node.status?.addresses?.find((a) => a.type === type)?.address;
	const facts: [string, string | undefined][] = [
		['Internal IP', address('InternalIP')],
		['External IP', address('ExternalIP')],
		['Hostname', address('Hostname')],
		['OS image', info?.osImage],
		['Kernel', info?.kernelVersion],
		['Container runtime', info?.containerRuntimeVersion],
		['Kubelet', info?.kubeletVersion],
		['Architecture', info?.architecture],
		['Created', node.metadata.creationTimestamp]
	];
	return facts.flatMap(([label, value]) => (value ? [{ label, value }] : []));
}

/** A taint as `key=value:Effect`. */
export function taintText(t: { key: string; value?: string; effect: string }): string {
	const value = t.value ? '=' + t.value : '';
	return t.key + value + ':' + t.effect;
}

/** Whether pods in this phase are fine: Running, or finished well. */
export function podPhaseOk(phase: string): boolean {
	return phase === 'Running' || phase === 'Succeeded';
}

