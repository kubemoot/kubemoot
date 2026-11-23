import type { RequestHandler } from './$types';
import type { AcceleratorUseResponse } from '#lib/accelerator-use.js';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { listNodes } from '#lib/server/k8s/index.js';
import { prometheusUrl } from '#lib/server/mode.js';
import { readAcceleratorUse, visibleTo } from '#lib/server/accelerator-metrics/index.js';

const headers = { 'Cache-Control': 'no-store' };

function answer(body: AcceleratorUseResponse): Response {
	return Response.json(body, { headers });
}

async function readUse(url: string, names: ReadonlySet<string>): Promise<AcceleratorUseResponse> {
	try {
		return { configured: true, use: { available: true, data: visibleTo(names, await readAcceleratorUse(url)) } };
	} catch (e) {
		const why = e instanceof Error ? e.message : 'unknown error';
		return { configured: true, use: { available: false, reason: `the metrics source could not be read (${why})` } };
	}
}

/**
 * Accelerator busy and memory per node from the metrics source named by metrics.prometheusUrl. Prometheus cannot
 * carry a Kubernetes identity, so the server reads it with its own access, asks for node-level accelerator
 * measures only, and answers only for the nodes the caller can list with their own token.
 */
export const GET: RequestHandler = async () => {
	const url = prometheusUrl();
	if (!url) return answer({ configured: false });
	try {
		const names = new Set((await listNodes()).map((n) => n.metadata.name));
		return answer(await readUse(url, names));
	} catch (e) {
		return kubeFailure(e, 'Failed to list nodes');
	}
};
