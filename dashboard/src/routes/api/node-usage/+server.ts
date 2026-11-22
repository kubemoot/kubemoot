import type { RequestHandler } from './$types';
import { readNodeMetrics, readPodCounts } from '#lib/server/k8s/node-metrics.js';

/**
 * CPU and memory use (metrics.k8s.io) and pod counts for every node, read with the caller's token.
 * Each reading says why it is missing when the cluster does not serve it or the caller may not read
 * it, so a page never shows a zero for a failed read.
 */
export const GET: RequestHandler = async () => {
	const [metrics, pods] = await Promise.all([readNodeMetrics(), readPodCounts()]);
	return Response.json({ metrics, pods }, { headers: { 'Cache-Control': 'no-store' } });
};
