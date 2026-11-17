// Ready-of-total counts for the Overview page cards.

export interface CountEntry {
	total: number;
	ready: number;
}

interface NodeLike {
	gpu?: { present: boolean };
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

/** Node counts (Ready condition) and GPU node counts; every GPU node counts as ready. */
export function nodeCounts(nodes: NodeLike[]): { nodes: CountEntry; gpus: CountEntry } {
	const gpuNodes = nodes.filter((n) => n.gpu?.present).length;
	return {
		nodes: { total: nodes.length, ready: nodes.filter(isNodeReady).length },
		gpus: { total: gpuNodes, ready: gpuNodes }
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
