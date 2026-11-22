import type { RequestHandler } from './$types';
import { gateRequest, parseAsk, proxyGateway, refusal } from '#lib/server/gateway.js';

/**
 * POST /api/gateway/{namespace}/{crew}/ask
 *
 * Asks the crew a question as the signed-in person: a new conversation, or a follow-up when
 * the body names a conversationId. The question goes to the crew's discussion gateway with the
 * person's own bearer token, exactly as kmctl sends it, so the gateway's access rules and
 * ownership apply and the conversation belongs to the person. The gateway's refusals (401, 403,
 * 404 for a conversation that is not theirs) come back with the same status.
 */
export const POST: RequestHandler = async ({ params, request }) => {
	const parsed = parseAsk(await request.json().catch(() => null));
	if (!parsed.ok) return refusal(400, parsed.error);
	const stopped = await gateRequest(params.namespace, params.crew);
	if (stopped) return stopped;
	return proxyGateway(
		params.namespace,
		params.crew,
		`/api/v1/discussions/${params.crew}`,
		{ method: 'POST', body: parsed.body },
		async (upstream) => Response.json(await upstream.json())
	);
};
