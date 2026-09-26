import type { RequestHandler } from './$types';
import { crdWatchResponse } from '$lib/server/k8s/watch-sse';

/**
 * GET /api/kubemoot/fitness/watch?namespace=<ns>
 *
 * Combined live watch for the Fitness page: streams CrewFitnessSuite +
 * CrewFitness changes as SSE so the page updates push-style (no polling). See
 * crdWatchResponse for the event shape and lifecycle.
 */
export const GET: RequestHandler = ({ url }) => {
	const ns = url.searchParams.get('namespace') || '';
	return crdWatchResponse(['crewfitnesssuites', 'crewfitnesses'], ns);
};
