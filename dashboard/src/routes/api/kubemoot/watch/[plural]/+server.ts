import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { crdWatchResponse, isWatchableCrd } from '$lib/server/k8s/watch-sse';

/**
 * GET /api/kubemoot/watch/{plural}?namespace=<ns>
 *
 * Generic live watch for any kubemoot CRD list page. Streams ADDED/MODIFIED/
 * DELETED events as SSE so pages update push-style instead of polling. The
 * plural is validated against the known CRD set (no arbitrary watch paths).
 */
export const GET: RequestHandler = ({ params, url }) => {
	const plural = params.plural;
	if (!isWatchableCrd(plural)) {
		return json({ error: `unknown resource: ${plural}` }, { status: 404 });
	}
	const ns = url.searchParams.get('namespace') || '';
	return crdWatchResponse([plural], ns);
};
