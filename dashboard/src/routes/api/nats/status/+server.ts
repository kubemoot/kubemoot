import type { RequestHandler } from './$types';
import { readNatsStatus } from '#lib/server/nats-status.js';
import { cachedRead } from '#lib/server/ttl-cache.js';
import { scopedNamespaces } from '#lib/server/scope.js';

const status = cachedRead(5000, readNatsStatus);

/**
 * Server, JetStream account and every store, read only. Stores the dashboard may not
 * serve in a namespace-scoped instance are left out by readNatsStatus.
 *
 * GET /api/nats/status
 */
export const GET: RequestHandler = async () => {
	try {
		await scopedNamespaces();
	} catch {
		return Response.json({ error: 'namespace scope unavailable' }, { status: 503 });
	}
	return Response.json(await status(), { headers: { 'Cache-Control': 'no-store' } });
};
