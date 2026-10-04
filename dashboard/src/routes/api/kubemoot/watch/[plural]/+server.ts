import type { RequestHandler } from './$types';
import { crdWatchResponse, isWatchableCrd } from '#lib/server/k8s/watch-sse.js';
import { guardNamespaceOrAll, objectAccepter, scopeKindFor } from '#lib/server/scope.js';

/**
 * GET /api/kubemoot/watch/{plural}?namespace=<ns>
 *
 * Generic live watch for any kubemoot CRD list page. Streams ADDED/MODIFIED/
 * DELETED events as SSE so pages update push-style instead of polling. The
 * plural is validated against the known CRD set (no arbitrary watch paths).
 */
export const GET: RequestHandler = async ({ params, url }) => {
	const plural = params.plural;
	if (!isWatchableCrd(plural)) {
		return Response.json({ error: `unknown resource: ${plural}` }, { status: 404 });
	}
	const ns = url.searchParams.get('namespace') || '';
	const kind = scopeKindFor(plural);
	const denied = await guardNamespaceOrAll(ns, kind);
	if (denied) return denied;
	return crdWatchResponse([plural], ns, objectAccepter(kind));
};
