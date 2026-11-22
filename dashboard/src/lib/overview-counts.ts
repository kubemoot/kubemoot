// Ready-of-total counts for the Overview page cards.

import { nodeAccelerator } from './node-accelerator';

export interface CountEntry {
	total: number;
	ready: number;
}

interface NodeLike {
	accelerator?: { present: boolean; count?: number };
	/** Deprecated old name of accelerator. */
	gpu?: { present: boolean; count?: number };
	status?: { conditions?: Array<{ type: string; status: string }> };
}

/** How many of the resources there are and how many report status.ready. */
export function readyCount(items: Array<{ status?: { ready?: boolean } }> | undefined): CountEntry {
	return {
		total: items?.length || 0,
		ready: items?.filter((item) => item.status?.ready).length || 0
	};
}

function isNodeReady(node: NodeLike): boolean {
	return !!node.status?.conditions?.some((c) => c.type === 'Ready' && c.status === 'True');
}

/** Accelerators on a node: the device count its plugin reports, at least one on an accelerator node. */
function acceleratorsOn(node: NodeLike): number {
	const accelerator = nodeAccelerator(node);
	return accelerator?.present ? Math.max(accelerator.count ?? 1, 1) : 0;
}

/**
 * Node counts (Ready condition) and accelerator counts. An accelerator is one device of any
 * kind the cluster reports (GPU, TPU, NPU); it counts as ready when its node is Ready.
 */
export function nodeCounts(nodes: NodeLike[]): { nodes: CountEntry; accelerators: CountEntry } {
	const readyNodes = nodes.filter(isNodeReady);
	const sum = (list: NodeLike[]) => list.reduce((total, n) => total + acceleratorsOn(n), 0);
	return {
		nodes: { total: nodes.length, ready: readyNodes.length },
		accelerators: { total: sum(nodes), ready: sum(readyNodes) }
	};
}

/** The resources the cluster owns, shared by every crew: they live outside the crews' namespaces. */
const CLUSTER_OWNED = new Set(['modelservers', 'models', 'embeddingmodels']);

/**
 * The namespace to list a resource in when a crew is selected on the Overview. Model
 * servers, models, and embedding models belong to the cluster, so they are counted
 * across all namespaces whatever crew is selected; every other resource is the crew's.
 */
export function overviewNamespace(plural: string, selected: string): string {
	return CLUSTER_OWNED.has(plural) ? '' : selected;
}
