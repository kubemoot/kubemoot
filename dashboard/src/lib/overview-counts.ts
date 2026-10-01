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
