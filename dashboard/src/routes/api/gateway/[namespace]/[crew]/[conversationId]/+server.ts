import type { RequestHandler } from './$types';
import { accessReasonHeaders, gateRequest, proxyGateway, refusal, relayRefusal } from '#lib/server/gateway.js';

/**
 * DELETE /api/gateway/{namespace}/{crew}/{conversationId}[?force=true]
 *
 * Deletes a conversation from every store that holds it, as the signed-in person, and returns
 * the gateway's erasure report: what was removed per store, what could not be, what is kept.
 * A running conversation is a 409 unless force=true (which stops it first). When a store could
 * not be cleared the gateway answers 500 with the report, and it comes back unchanged so the
 * person can see what is left and repeat the delete.
 */
export const DELETE: RequestHandler = async ({ params, request, url }) => {
	const reason = accessReasonHeaders(request);
	if (!reason.ok) return refusal(400, reason.error);
	const stopped = await gateRequest(params.namespace, params.crew, params.conversationId);
	if (stopped) return stopped;
	const force = url.searchParams.get('force') === 'true' ? '?force=true' : '';
	return proxyGateway(
		params.namespace,
		params.crew,
		`/api/v1/discussions/${params.crew}/${params.conversationId}${force}`,
		{ method: 'DELETE', headers: reason.headers },
		async (upstream) => {
			const report: unknown = await upstream.json().catch(() => null);
			if (report && typeof report === 'object') {
				return Response.json(report, { status: upstream.status, headers: { 'Cache-Control': 'no-store' } });
			}
			return upstream.ok ? refusal(502, 'The crew deleted the conversation but sent no report.') : relayRefusal(upstream);
		},
		(status) => status === 500
	);
};
