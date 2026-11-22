import type { RequestHandler } from './$types';
import { gateRequest, proxyGateway } from '#lib/server/gateway.js';

/**
 * GET /api/gateway/{namespace}/{crew}/conversations
 *
 * The conversations the signed-in person started with this crew, listed by the crew's
 * discussion gateway with their own token: the gateway shows a person only their own.
 */
export const GET: RequestHandler = async ({ params, url }) => {
	const stopped = await gateRequest(params.namespace, params.crew);
	if (stopped) return stopped;
	const limit = url.searchParams.get('limit');
	const query = limit && /^\d{1,3}$/.test(limit) ? `?limit=${limit}` : '';
	return proxyGateway(params.namespace, params.crew, `/api/v1/discussions/${params.crew}${query}`, {}, async (upstream) =>
		Response.json(await upstream.json(), { headers: { 'Cache-Control': 'no-store' } })
	);
};
