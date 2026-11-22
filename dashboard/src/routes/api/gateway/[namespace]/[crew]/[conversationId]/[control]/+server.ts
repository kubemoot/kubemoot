import type { RequestHandler } from './$types';
import { CONTROLS } from '#lib/conversation-actions-client.js';
import { accessReasonHeaders, gateRequest, proxyGateway, refusal } from '#lib/server/gateway.js';

/**
 * POST /api/gateway/{namespace}/{crew}/{conversationId}/{stop|pause|resume}
 *
 * Stops, pauses or resumes a conversation's running discussion as the signed-in person. The
 * gateway admits the owner, or an operator who gives a reason (Kubemoot-Access-Reason), and
 * answers 409 when nothing is running; both come back with the same status.
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
		async (upstream) => Response.json(await upstream.json())
	);
};
