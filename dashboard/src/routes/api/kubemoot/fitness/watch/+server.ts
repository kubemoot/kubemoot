import type { RequestHandler } from './$types';
import { crdWatchResponse } from '$lib/server/k8s/watch-sse';
import { guardNamespaceOrAll, objectAccepter } from '$lib/server/scope';

/**
 * GET /api/kubemoot/fitness/watch?namespace=<ns>
 *
 * Combined live watch for the Fitness page: streams CrewFitnessSuite +
 * CrewFitness changes as SSE so the page updates push-style (no polling). See
 * crdWatchResponse for the event shape and lifecycle.
 */
export const GET: RequestHandler = async ({ url }) => {
	const ns = url.searchParams.get('namespace') || '';
	const denied = await guardNamespaceOrAll(ns);
	if (denied) return denied;
	return crdWatchResponse(['crewfitnesssuites', 'crewfitnesses'], ns, objectAccepter());
};
