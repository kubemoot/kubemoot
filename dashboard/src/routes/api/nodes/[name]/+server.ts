import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { getNode } from '#lib/server/k8s/index.js';
import { jsonError } from '#lib/server/json-error.js';
import { isNodeName, readNodeMetrics, readNodePods } from '#lib/server/k8s/node-metrics.js';

/** One node with its use and the pods on it; the use and the pods say why they are missing when they are. */
export const GET: RequestHandler = async ({ params }) => {
	if (!isNodeName(params.name)) return jsonError(400, 'Not a node name');
	try {
		const node = await getNode(params.name);
		const [metrics, pods] = await Promise.all([readNodeMetrics(params.name), readNodePods(params.name)]);
		return Response.json({ node, metrics, pods }, { headers: { 'Cache-Control': 'no-store' } });
	} catch (e) {
		return kubeFailure(e, 'Failed to read node');
	}
};
