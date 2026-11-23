// What a node tile on the Nodes page shows: its state and why, how much CPU, memory and pod room it
// has used, and whether its accelerator is available to Kubemoot and working. Everything else lives on the node's page.

import { acceleratorStatus, PODS_OFFLINE_TEXT, type AcceleratorStatus } from './accelerator-status.js';
import type { AcceleratorUseByNode } from './accelerator-use.js';
import { cores, gib, isNodeReady, isOffline, nodeHealth, nodeProblems, nodeUse, usageOf, type NodeMetric, type NodeUse } from './node-usage.js';
import { severityRank, type Health } from './health.js';
import type { PodCount } from './reading.js';
import { bar, usageBar, type TileBar } from './usage-bar.js';
import type { NodeWithAccelerator } from './types/k8s.js';
import type { ModelServer } from './types/kubemoot.js';

export interface NodeSummary {
	name: string;
	health: Health;
	/** Why the node is not healthy; empty when it is. */
	problems: string[];
	/** Taken out on purpose (cordoned and not Ready): drawn quietly, not as a fault. */
	offline: boolean;
	cpu?: TileBar;
	memory?: TileBar;
	pods?: TileBar;
	/** Said in place of the pods bar when the node is offline and its pods cannot be known. */
	podsNote?: string;
	accelerator?: AcceleratorStatus;
}

/** What the accelerator section needs besides the node: the model servers and the measured use. */
export interface AcceleratorContext {
	servers?: readonly ModelServer[];
	use?: AcceleratorUseByNode;
}

/** The pods bar, or the note that stands in for it: a node that is not Ready reports pods it can no longer vouch for. */
function podsParts(node: NodeWithAccelerator, pods: PodCount | undefined, podsRead: boolean): Pick<NodeSummary, 'pods' | 'podsNote'> {
	if (!podsRead) return {};
	return isNodeReady(node) ? { pods: podBar(node, pods) } : { podsNote: PODS_OFFLINE_TEXT };
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
	podsRead: boolean,
	context: AcceleratorContext = {}
): NodeSummary {
	const use: NodeUse = nodeUse(node, metric);
	return {
		name: node.metadata.name,
		health: nodeHealth(node, use),
		problems: nodeProblems(node, use).map((p) => p.text),
		offline: isOffline(node),
		cpu: use.cpu && usageBar('CPU', use.cpu, cores, ' cores'),
		memory: use.memory && usageBar('Memory', use.memory, gib),
		...podsParts(node, pods, podsRead),
		accelerator: acceleratorStatus({ node, holders: podsRead ? (pods?.holders ?? []) : undefined, servers: context.servers, use: context.use })
	};
}

/** Unhealthiest first, then by name, so the node that needs a look is the first tile. */
export function bySeverityThenName(a: NodeSummary, b: NodeSummary): number {
	return severityRank(b.health) - severityRank(a.health) || a.name.localeCompare(b.name);
}
