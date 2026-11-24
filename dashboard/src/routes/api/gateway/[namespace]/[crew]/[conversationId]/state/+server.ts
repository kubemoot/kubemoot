import type { RequestHandler } from './$types';
import { accessReasonHeaders, gateRequest, proxyGateway, refusal } from '#lib/server/gateway.js';

/**
 * GET /api/gateway/{namespace}/{crew}/{conversationId}/state
 *
 * Where a conversation and each of its turns stand now (queued with its place in the queue,
 * running, paused, stopped, finished), as the signed-in person. It answers for a conversation
 * another client started and for a question no agent has seen, so the page offers Stop, Pause
 * and Cancel from it instead of guessing from its own panel.
 */
export const GET: RequestHandler = async ({ params, request }) => {
	const reason = accessReasonHeaders(request);
	if (!reason.ok) return refusal(400, reason.error);
	const stopped = await gateRequest(params.namespace, params.crew, params.conversationId);
	if (stopped) return stopped;
	return proxyGateway(
		params.namespace,
		params.crew,
		`/api/v1/discussions/${params.crew}/${params.conversationId}/state`,
		{ headers: reason.headers },
		async (upstream) => Response.json(await upstream.json(), { headers: { 'Cache-Control': 'no-store' } })
	);
};
