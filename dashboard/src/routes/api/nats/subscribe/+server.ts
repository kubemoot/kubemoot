import type { RequestHandler } from './$types';
import type { Msg } from 'nats';
import { getNatsConnection, sc } from '$lib/server/nats-client';
import { relayToSse, sseResponse } from '$lib/server/sse';
import { forbidden, messageSubjectAllowed, subjectFilterAllowed } from '$lib/server/scope';

/**
 * SSE endpoint that subscribes to a NATS subject and streams messages to the browser.
 * This proxies NATS messages through the SvelteKit server, avoiding the need for
 * direct browser-to-NATS WebSocket connections (which fail due to mixed content
 * when the dashboard is served over HTTPS). The subscription is unsubscribed when
 * the browser disconnects.
 *
 * Usage: GET /api/nats/subscribe?subject=kubemoot.>
 */
export const GET: RequestHandler = async ({ url }) => {
	const subject = url.searchParams.get('subject') || 'kubemoot.>';
	if (!(await subjectFilterAllowed(subject))) return forbidden('subject');

	return sseResponse(async (sink) => {
		const nc = await getNatsConnection();
		const sub = nc.subscribe(subject);
		sink.data({ type: 'connected', subject });
		void relayToSse(sub, sink, (msg: Msg) =>
			messageSubjectAllowed(msg.subject)
				? {
						type: 'message',
						subject: msg.subject,
						data: sc.decode(msg.data),
						timestamp: new Date().toISOString()
					}
				: undefined
		);
		return () => sub.unsubscribe();
	});
};
