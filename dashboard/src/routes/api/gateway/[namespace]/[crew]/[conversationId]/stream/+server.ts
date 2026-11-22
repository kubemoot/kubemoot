import type { RequestHandler } from './$types';
import { gateRequest, proxyGateway, refusal } from '#lib/server/gateway.js';

/**
 * GET /api/gateway/{namespace}/{crew}/{conversationId}/stream
 *
 * Follows a conversation's answer as the signed-in person: the crew's gateway streams it
 * (server-sent events) after checking their token and that the conversation is theirs, and
 * the dashboard relays the stream unchanged. It ends when the person's session does.
 * `since` and Last-Event-ID resume a stream.
 */
export const GET: RequestHandler = async ({ params, url, request }) => {
	const stopped = await gateRequest(params.namespace, params.crew, params.conversationId);
	if (stopped) return stopped;
	const since = url.searchParams.get('since');
	const query = since ? `?since=${encodeURIComponent(since)}` : '';
	const resume = request.headers.get('last-event-id');
	return proxyGateway(
		params.namespace,
		params.crew,
		`/api/v1/discussions/${params.crew}/${params.conversationId}/stream${query}`,
		{ signal: request.signal, headers: resume ? { 'Last-Event-ID': resume } : {} },
		(upstream) =>
			upstream.body
				? new Response(upstream.body, {
						headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', 'X-Accel-Buffering': 'no' }
					})
				: refusal(502, "The crew's gateway sent no stream.")
	);
};
