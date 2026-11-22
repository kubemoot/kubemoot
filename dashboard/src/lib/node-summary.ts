// What a node tile on the Nodes page shows: its state and why, how much CPU, memory and pod room it
// has used, and how much of its accelerator is in use. Everything else lives on the node's page.

import { nodeAccelerator } from './node-accelerator.js';
import { cores, gib, nodeHealth, nodeProblems, nodeUse, usageOf, type NodeMetric, type NodeUse } from './node-usage.js';
import { severityRank, type Health } from './health.js';
import type { PodCount } from './reading.js';
import { bar, usageBar, type TileBar } from './usage-bar.js';
import type { NodeWithAccelerator } from './types/k8s.js';

export interface AcceleratorSummary {
	/** "NVIDIA GeForce RTX 5090 x1, 32GB". */
	label: string;
	/** Devices pods have asked for against devices the node has; undefined when pods could not be read. */
	use?: TileBar;
}

export interface NodeSummary {
	name: string;
	health: Health;
	/** Why the node is not healthy; empty when it is. */
	problems: string[];
	cpu?: TileBar;
	memory?: TileBar;
	pods?: TileBar;
	accelerator?: AcceleratorSummary;
}

function acceleratorSummary(node: NodeWithAccelerator, pods: PodCount | undefined, podsRead: boolean): AcceleratorSummary | undefined {
	const accelerator = nodeAccelerator(node);
	if (!accelerator?.present) return undefined;
	const count = accelerator.count ?? 1;
	const label = [accelerator.type || 'Accelerator', count > 1 ? `x${count}` : undefined, accelerator.memory].filter(Boolean).join(' ');
	if (!podsRead) return { label };
	const asked = Math.min(pods?.accelerators ?? 0, count);
	return { label, use: { ...bar('In use', `${asked} of ${count}`, asked / count), level: 'normal' } };
}

function podBar(node: NodeWithAccelerator, pods: PodCount | undefined): TileBar | undefined {
	// A node with no pod in the list holds none: its count is zero, since the pods were read.
	const limit = usageOf(String(pods?.total ?? 0), node.status?.allocatable?.pods);
	if (!limit) return undefined;
	return bar('Pods', `${pods?.running ?? 0} running, ${limit.used} of ${limit.allocatable}`, limit.fraction);
}

/** The tile for one node. `metric` and `pods` are undefined when that reading is missing; the bar is then left out. */
export function summarizeNode(
	node: NodeWithAccelerator,
	metric: NodeMetric | undefined,
	pods: PodCount | undefined,
	podsRead: boolean
): NodeSummary {
	const use: NodeUse = nodeUse(node, metric);
	return {
		name: node.metadata.name,
		health: nodeHealth(node, use),
		problems: nodeProblems(node, use).map((p) => p.text),
		cpu: use.cpu && usageBar('CPU', use.cpu, cores, ' cores'),
		memory: use.memory && usageBar('Memory', use.memory, gib),
		pods: podsRead ? podBar(node, pods) : undefined,
		accelerator: acceleratorSummary(node, pods, podsRead)
	};
}

/** Unhealthiest first, then by name, so the node that needs a look is the first tile. */
export function bySeverityThenName(a: NodeSummary, b: NodeSummary): number {
	return severityRank(b.health) - severityRank(a.health) || a.name.localeCompare(b.name);
}
