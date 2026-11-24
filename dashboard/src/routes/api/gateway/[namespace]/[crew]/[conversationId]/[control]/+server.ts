import type { RequestHandler } from './$types';
import { CONTROLS } from '#lib/conversation-actions-client.js';
import { accessReasonHeaders, gateRequest, proxyGateway, refusal } from '#lib/server/gateway.js';

/**
 * POST /api/gateway/{namespace}/{crew}/{conversationId}/{stop|pause|resume}
 *
 * Stops, pauses or resumes a conversation's running discussion as the signed-in person. The
 * gateway admits the owner, or an operator who gives a reason (Kubemoot-Access-Reason). A stop
 * answers with what happened (outcome and message); 202 means the crew did not confirm it, and
 * 409 that the crew has just taken the question. Each comes back with the gateway's status.
 */
export const POST: RequestHandler = async ({ params, request }) => {
	if (!(CONTROLS as readonly string[]).includes(params.control)) return refusal(404, 'Not found.');
	const reason = accessReasonHeaders(request);
	if (!reason.ok) return refusal(400, reason.error);
	const stopped = await gateRequest(params.namespace, params.crew, params.conversationId);
	if (stopped) return stopped;
	return proxyGateway(
		params.namespace,
		params.crew,
		`/api/v1/discussions/${params.crew}/${params.conversationId}/${params.control}`,
		{ method: 'POST', headers: reason.headers },
		async (upstream) => Response.json(await upstream.json(), { status: upstream.status })
	);
};
